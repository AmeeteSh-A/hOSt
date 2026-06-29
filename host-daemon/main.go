package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
	"github.com/bytecodealliance/wasmtime-go/v45"
	"github.com/ledongthuc/pdf"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/sashabaranov/go-openai"
)

type DaemonRoute struct {
	AppType string `json:"app_type"`
}

type KernelComponent struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	Text        string `json:"text"`
	TargetWasm  string `json:"target_wasm"`
	Placeholder string `json:"placeholder"`
	Min         any    `json:"min"`
	Max         any    `json:"max"`
	Value       any    `json:"value"`
}

type KernelLayout struct {
	Hierarchy []KernelComponent `json:"hierarchy"`
}

type ChartDataPoint struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Ratio float64 `json:"ratio"`
}

type PDFTask struct {
	FileIndex int      `json:"file_idx"`
	Pages     []uint32 `json:"pages"`
}

type PDFCommand struct {
	OpID  uint32    `json:"op_id"`
	Tasks []PDFTask `json:"tasks"`
}

type WasmHeader struct {
	OpID    uint32   `json:"op_id"`
	Pages   []uint32 `json:"pages"`
	PdfSize uint32   `json:"pdf_size"`
}

func getFloat(v any) float64 {
	if v == nil {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return val
	case string:
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return 0
		}
		return f
	default:
		return 0
	}
}

func cloneImage(src *image.RGBA) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	copy(dst.Pix, src.Pix)
	return dst
}

func generateSampleCSV() {
	if _, err := os.Stat("data.csv"); os.IsNotExist(err) {
		sampleData := "id,product_name,revenue,cost\n1,Alpha Engine,1500.50,400.00\n2,Beta Transpiler,2200.75,600.50\n3,Gamma DB,3400.00,1200.00\n4,Delta UI,800.25,250.00"
		os.WriteFile("data.csv", []byte(sampleData), 0644)
	}
}

func createTarArchive(src string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	defer tw.Close()
	info, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	var baseDir string
	if info.IsDir() {
		baseDir = filepath.Base(src)
	}
	err = filepath.Walk(src, func(file string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(fi, fi.Name())
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(src, file)
		if err != nil {
			return err
		}
		if info.IsDir() {
			header.Name = filepath.Join(baseDir, relPath)
		} else {
			header.Name = filepath.Base(src)
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !fi.IsDir() {
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if _, err := tw.Write(data); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func extractTarArchive(tarBytes []byte, destDir string) error {
	tr := tar.NewReader(bytes.NewReader(tarBytes))
	for {
		header, err := tr.Next()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return err
		}
		target := filepath.Join(destDir, header.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(os.PathSeparator)) {
 		   continue
		}
		if header.Typeflag == tar.TypeDir {
			os.MkdirAll(target, 0755)
		} else if header.Typeflag == tar.TypeReg {
			os.MkdirAll(filepath.Dir(target), 0755)
			f, err := os.Create(target)
			if err != nil {
				return err
			}
			io.Copy(f, tr)
			f.Close()
		}
	}
	return nil
}

func getUniqueFilePath(baseDir, baseName, ext string) string {
	finalPath := filepath.Join(baseDir, baseName+ext)
	if _, err := os.Stat(finalPath); os.IsNotExist(err) {
		return finalPath
	}
	for i := 1; ; i++ {
		finalPath = filepath.Join(baseDir, fmt.Sprintf("%s_%d%s", baseName, i, ext))
		if _, err := os.Stat(finalPath); os.IsNotExist(err) {
			return finalPath
		}
	}
}

func executeWasmModule(targetPath string, payload []byte) []byte {
	engine := wasmtime.NewEngine()
	store := wasmtime.NewStore(engine)
	wasmBytes, err := os.ReadFile(targetPath)
	if err != nil {
		fmt.Println("CRITICAL KERNEL ERROR: Could not find binary at", targetPath)
		return payload
	}
	module, err := wasmtime.NewModule(engine, wasmBytes)
	if err != nil {
		panic(err)
	}
	linker := wasmtime.NewLinker(engine)
	instance, err := linker.Instantiate(store, module)
	if err != nil {
		panic(err)
	}
	allocFunc := instance.GetFunc(store, "alloc")
	processFunc := instance.GetFunc(store, "execute")
	memory := instance.GetExport(store, "memory").Memory()
	argSize := int32(len(payload))
	allocResult, _ := allocFunc.Call(store, argSize)
	wPtr := allocResult.(int32)
	memData := memory.UnsafeData(store)
	copy(memData[wPtr:wPtr+argSize], payload)
	val, _ := processFunc.Call(store, wPtr, argSize)
	outSize := argSize
	if val != nil {
		if returnLen, ok := val.(int32); ok {
			outSize = returnLen
		}
	}
	outputBytes := make([]byte, outSize)
	copy(outputBytes, memData[wPtr:wPtr+outSize])
	return outputBytes
}

func routeDaemonIntent(intent string, apiKey string) string {
	config := openai.DefaultConfig(apiKey)
	config.BaseURL = "https://api.groq.com/openai/v1"
	client := openai.NewClientWithConfig(config)
	systemPrompt := `You are the Master OS Daemon. Determine what type of sandbox the user wants to spawn.
If the request involves photos, images, or editing pictures, return "image".
If the request involves spreadsheets, documents, data, or CSVs, return "csv".
If the request involves zipping, compressing, archiving, extracting, unzipping or folders, return "archive".
If the request involves PDFs or documents, return "pdf".
If the request involves encryption, decryption, passwords, or security, return "crypto".
If the request involves markdown, text editing, writing, or transpiling, return "markdown".
Otherwise return "unknown".
Output ONLY valid JSON: {"app_type": "string"}`
	resp, err := client.CreateChatCompletion(context.Background(), openai.ChatCompletionRequest{
		Model:          "llama-3.3-70b-versatile",
		ResponseFormat: &openai.ChatCompletionResponseFormat{Type: openai.ChatCompletionResponseFormatTypeJSONObject},
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: intent},
		},
	})
	if err != nil {
		return "unknown"
	}
	var route DaemonRoute
	json.Unmarshal([]byte(strings.TrimSpace(resp.Choices[0].Message.Content)), &route)
	return route.AppType
}

func parsePDFCommand(intent string, apiKey string, numFiles int) PDFCommand {
	config := openai.DefaultConfig(apiKey)
	config.BaseURL = "https://api.groq.com/openai/v1"
	client := openai.NewClientWithConfig(config)
	systemPrompt := fmt.Sprintf(`You are the PDF Command Router. Parse the user's intent involving %d files (indexed 0 to %d).
If EXTRACT/SPLIT/KEEP: op_id = 0.
If READ/SCAN TEXT: op_id = 1.
If MERGE/COMBINE/BIND: op_id = 2.
Map operations to tasks. Empty pages array [] means ALL pages.
Output ONLY valid JSON: {"op_id": 2, "tasks": [{"file_idx": 0, "pages": [2,3,4]}, {"file_idx": 1, "pages": []}]}`, numFiles, numFiles-1)

	resp, err := client.CreateChatCompletion(context.Background(), openai.ChatCompletionRequest{
		Model:          "llama-3.3-70b-versatile",
		ResponseFormat: &openai.ChatCompletionResponseFormat{Type: openai.ChatCompletionResponseFormatTypeJSONObject},
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: intent},
		},
	})
	if err != nil {
		return PDFCommand{}
	}
	var cmd PDFCommand
	json.Unmarshal([]byte(strings.TrimSpace(resp.Choices[0].Message.Content)), &cmd)
	return cmd
}

func fetchLocalSandboxUI(appType string, intent string, apiKey string) KernelLayout {
	config := openai.DefaultConfig(apiKey)
	config.BaseURL = "https://api.groq.com/openai/v1"
	client := openai.NewClientWithConfig(config)
	systemPrompt := fmt.Sprintf(`You are generating UI for an isolated '%s' Sandbox.

STRICT Wasm Targets for Image:
- "../compiled-binaries/image_filter.wasm"
- "../compiled-binaries/invert.wasm" 
- "../compiled-binaries/crop.wasm" 
- "../compiled-binaries/resize.wasm" 
- "../compiled-binaries/brightness.wasm" 

STRICT Wasm Targets for CSV:
- "../compiled-binaries/csv_engine.wasm"

Output ONLY valid JSON:
{
    "hierarchy": [
        {
            "type": "input" | "slider" | "button",
            "id": "exact_id_matching_rules",
            "text": "Label",
            "placeholder": "For inputs",
            "target_wasm": "path_to_wasm",
            "min": 0,
            "max": 100,
            "value": 0
        }
    ]
}`, appType)
	resp, err := client.CreateChatCompletion(context.Background(), openai.ChatCompletionRequest{
		Model:          "llama-3.3-70b-versatile",
		ResponseFormat: &openai.ChatCompletionResponseFormat{Type: openai.ChatCompletionResponseFormatTypeJSONObject},
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: intent},
		},
	})
	if err != nil {
		return KernelLayout{}
	}
	var layout KernelLayout
	json.Unmarshal([]byte(strings.TrimSpace(resp.Choices[0].Message.Content)), &layout)
	return layout
}

func renderNativeVectorChart(fyneApp fyne.App, points []ChartDataPoint) {
	win := fyneApp.NewWindow("Vector Data Chart")
	win.Resize(fyne.NewSize(700, 450))
	var bars []fyne.CanvasObject
	for _, pt := range points {
		barColor := color.RGBA{R: 59, G: 130, B: 246, A: 255}
		rect := canvas.NewRectangle(barColor)
		rect.SetMinSize(fyne.NewSize(50, float32(pt.Ratio*300)))
		valLabel := widget.NewLabel(fmt.Sprintf("%.1f", pt.Value))
		valLabel.Alignment = fyne.TextAlignCenter
		lbl := widget.NewLabel(pt.Label)
		lbl.Alignment = fyne.TextAlignCenter
		lbl.Truncation = fyne.TextTruncateEllipsis
		col := container.NewVBox(layout.NewSpacer(), valLabel, rect, lbl)
		bars = append(bars, col)
	}
	scroller := container.NewHScroll(container.NewHBox(bars...))
	win.SetContent(container.NewBorder(widget.NewLabel("In-Memory Computational Graph Visualization"), nil, nil, nil, scroller))
	win.Show()
}

func readPdfNative(path string, targetPages []uint32) (string, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var buf bytes.Buffer
	total := r.NumPage()

	pageSet := make(map[int]bool)
	for _, p := range targetPages {
		pageSet[int(p)] = true
	}

	for i := 1; i <= total; i++ {
		if len(targetPages) > 0 && !pageSet[i] {
			continue
		}
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		text, err := p.GetPlainText(nil)
		if err != nil {
			continue
		}
		buf.WriteString(text)
		buf.WriteString("\n\n--- PAGE BREAK ---\n\n")
	}
	return buf.String(), nil
}

const htmlDecrypterTemplate = `<!DOCTYPE html>
<html>
<head>
    <title>Zero-Trust Decryption Vault</title>
    <style>
        body { font-family: -apple-system, sans-serif; background: #b7b7b7; color: #f8fafc; display: flex; justify-content: center; align-items: center; height: 100vh; margin: 0; }
        .box { background: #a9a9a9; padding: 40px; border-radius: 12px; box-shadow: 0 4px 6px rgba(239, 239, 239, 0.3); text-align: center; max-width: 400px; }
        input[type="file"], input[type="password"] { margin: 15px 0; padding: 10px; width: 90%; border-radius: 6px; border: 1px solid #737373; background: #b3b3b3; color: white; }
        button { background: #515151; color: white; border: none; padding: 12px 24px; border-radius: 6px; cursor: pointer; font-weight: bold; width: 100%; transition: background 0.2s;}
        button:hover { background: #c7c8ca; }
        #status { margin-top: 15px; font-size: 14px; color: #000000; }
    </style>
</head>
<body>
    <div class="box">
        <h2>Decrypt File</h2>
        <p>Select (or dran n drop) the .enc file and enter the password.</p>
        <input type="file" id="fileInput">
        <input type="password" id="passInput" placeholder="Encryption Password">
        <button onclick="decryptFile()">Unlock & Download</button>
        <div id="status"></div>
    </div>

    <script>
        async function decryptFile() {
            const fileInput = document.getElementById('fileInput');
            const passInput = document.getElementById('passInput');
            const status = document.getElementById('status');

            if (fileInput.files.length === 0 || !passInput.value) {
                status.innerText = "Error: Provide both file and password.";
                return;
            }

            status.innerText = "Deriving cryptographic keys...";
            try {
                const fileData = await fileInput.files[0].arrayBuffer();
                const bytes = new Uint8Array(fileData);
                
                const salt = bytes.slice(0, 16);
                const nonce = bytes.slice(16, 28);
                const cipherText = bytes.slice(28);

                const encoder = new TextEncoder();
                const keyMaterial = await crypto.subtle.importKey(
                    "raw", encoder.encode(passInput.value), "PBKDF2", false, ["deriveKey"]
                );

                const key = await crypto.subtle.deriveKey(
                    { name: "PBKDF2", salt: salt, iterations: 100000, hash: "SHA-256" },
                    keyMaterial, { name: "AES-GCM", length: 256 }, false, ["decrypt"]
                );

                status.innerText = "Decrypting AES-256 payload...";
                const decryptedData = await crypto.subtle.decrypt(
                    { name: "AES-GCM", iv: nonce }, key, cipherText
                );

                const blob = new Blob([decryptedData]);
                const url = URL.createObjectURL(blob);
                const a = document.createElement('a');
                a.href = url;
                a.download = "decrypted_" + fileInput.files[0].name.replace('.enc', '');
                a.click();
                status.innerText = "Success! File downloaded.";
                status.style.color = "#4ade80";

            } catch (err) {
                console.error(err);
                status.innerText = "Decryption Failed: Incorrect password or corrupted file.";
                status.style.color = "#ef4444";
            }
        }
    </script>
</body>
</html>`

const instructionTxt = `///////////////////
Secure decryption instr.
/////////////

file has been secured using AES-256-GCM cryptography. 

decrypt instructions:
1. open the html file in the folder in any browswer (doesnt touch the network so doesnt matter (just uses browser's cryptography engine)).
3. Select the ".enc" file.
4. Enter your password.
5. It will decrypt the file locally and save it to your Downloads folder.

You can safely email or send this entire folder to anyone.`

func spawnCryptoSandbox(fyneApp fyne.App) {
	sandbox := fyneApp.NewWindow("Cryptography Sandbox (AES-256)")
	sandbox.Resize(fyne.NewSize(600, 350))

	pathEntry := widget.NewEntry()
	pathEntry.SetPlaceHolder("Enter absolute path to file to Encrypt/Decrypt...")

	passEntry := widget.NewPasswordEntry()
	passEntry.SetPlaceHolder("Enter Vault Password...")

	statusLabel := widget.NewLabel("Awaiting file. Math happens entirely in-memory.")
	statusLabel.Wrapping = fyne.TextWrapWord

	encryptBtn := widget.NewButton("Encrypt & Package Vault", func() {
		cleanPath := strings.Trim(strings.TrimSpace(pathEntry.Text), `"'`)
		password := passEntry.Text
		if cleanPath == "" || password == "" {
			statusLabel.SetText("Error: Need path and password.")
			return
		}

		fileBytes, err := os.ReadFile(cleanPath)
		if err != nil {
			statusLabel.SetText("Error reading file.")
			return
		}

		// Generate Cryptographic Math Parameters
		salt := make([]byte, 16)
		rand.Read(salt)
		nonce := make([]byte, 12)
		rand.Read(nonce)

		passBytes := []byte(password)
		passLen := uint32(len(passBytes))

		// Memory Payload: [OpID:4][PassLen:4][Pass][Salt:16][Nonce:12][FileBytes]
		bufferSize := 4 + 4 + len(passBytes) + 16 + 12 + len(fileBytes)
		payload := make([]byte, bufferSize)

		binary.LittleEndian.PutUint32(payload[0:4], 0) // OpID 0 = Encrypt
		binary.LittleEndian.PutUint32(payload[4:8], passLen)

		idx := 8
		copy(payload[idx:], passBytes)
		idx += len(passBytes)
		copy(payload[idx:], salt)
		idx += 16
		copy(payload[idx:], nonce)
		idx += 12
		copy(payload[idx:], fileBytes)

		statusLabel.SetText("Rust Wasm executing AES-256-GCM cipher...")
		res := executeWasmModule("../compiled-binaries/crypto_engine.wasm", payload)

		if len(res) == 0 {
			statusLabel.SetText("Engine Error: Encryption failed.")
			return
		}

		// Create the Vault Folder
		baseDir := filepath.Dir(cleanPath)
		baseName := filepath.Base(cleanPath)
		vaultPath := filepath.Join(baseDir, baseName+"_SECURE_VAULT")
		os.MkdirAll(vaultPath, 0755)

		// 1. Write the .enc file (Prepending Salt and Nonce so the Decrypter can find them)
		finalEncBytes := append(salt, nonce...)
		finalEncBytes = append(finalEncBytes, res...)
		os.WriteFile(filepath.Join(vaultPath, baseName+".enc"), finalEncBytes, 0644)

		// 2. Write the Portable HTML Decrypter
		os.WriteFile(filepath.Join(vaultPath, "Decrypt_Vault.html"), []byte(htmlDecrypterTemplate), 0644)

		// 3. Write the Instructions
		os.WriteFile(filepath.Join(vaultPath, "instructions.txt"), []byte(instructionTxt), 0644)

		statusLabel.SetText(fmt.Sprintf("SUCCESS!\nVault generated safely at:\n%s", vaultPath))
	})

	decryptBtn := widget.NewButton("Decrypt Local .enc File", func() {
		cleanPath := strings.Trim(strings.TrimSpace(pathEntry.Text), `"'`)
		password := passEntry.Text
		if cleanPath == "" || password == "" || !strings.HasSuffix(cleanPath, ".enc") {
			statusLabel.SetText("Error: Need .enc path and password.")
			return
		}

		fileBytes, err := os.ReadFile(cleanPath)
		if err != nil || len(fileBytes) < 28 {
			statusLabel.SetText("Error reading valid .enc file.")
			return
		}

		salt := fileBytes[0:16]
		nonce := fileBytes[16:28]
		cipherText := fileBytes[28:]

		passBytes := []byte(password)
		passLen := uint32(len(passBytes))

		bufferSize := 4 + 4 + len(passBytes) + 16 + 12 + len(cipherText)
		payload := make([]byte, bufferSize)

		binary.LittleEndian.PutUint32(payload[0:4], 1) // OpID 1 = Decrypt
		binary.LittleEndian.PutUint32(payload[4:8], passLen)

		idx := 8
		copy(payload[idx:], passBytes)
		idx += len(passBytes)
		copy(payload[idx:], salt)
		idx += 16
		copy(payload[idx:], nonce)
		idx += 12
		copy(payload[idx:], cipherText)

		statusLabel.SetText("Rust Wasm calculating decryption keys...")
		res := executeWasmModule("../compiled-binaries/crypto_engine.wasm", payload)

		if len(res) == 0 {
			statusLabel.SetText("Decryption Failed: Incorrect password or tampered file.")
			return
		}

		outName := strings.TrimSuffix(filepath.Base(cleanPath), ".enc")
		outPath := filepath.Join(filepath.Dir(cleanPath), "unlocked_"+outName)
		os.WriteFile(outPath, res, 0644)

		statusLabel.SetText(fmt.Sprintf("SUCCESS!\nFile unlocked and saved to:\n%s", outPath))
	})

	content := container.NewVBox(
		widget.NewLabel("Isolated AES-256 Zero-Trust Vault"),
		pathEntry,
		passEntry,
		container.NewGridWithColumns(2, encryptBtn, decryptBtn),
		widget.NewSeparator(),
		statusLabel,
	)

	sandbox.SetContent(container.NewCenter(content))
	sandbox.Show()
}

func spawnPDFSandbox(fyneApp fyne.App, apiKey string) {
	sandbox := fyneApp.NewWindow("PDF Sandbox")
	sandbox.Resize(fyne.NewSize(800, 600))

	var activePaths []string

	textArea := widget.NewMultiLineEntry()
	textArea.Wrapping = fyne.TextWrapWord
	textArea.SetPlaceHolder("Scanned text will appear here...")

	commandEntry := widget.NewEntry()
	commandEntry.SetPlaceHolder("E.g., 'Merge pages 1,3 of file 1 with page 5 of file 2'")

	executeBtn := widget.NewButton("Execute NLP Command", func() {
		if len(activePaths) == 0 {
			dialog.ShowInformation("Error", "No PDFs are mounted.", sandbox)
			return
		}

		cmd := parsePDFCommand(commandEntry.Text, apiKey, len(activePaths))
		if len(cmd.Tasks) == 0 {
			return
		}

		if cmd.OpID == 1 {
			var fullText string
			for _, task := range cmd.Tasks {
				if task.FileIndex >= len(activePaths) {
					continue
				}
				text, err := readPdfNative(activePaths[task.FileIndex], task.Pages)
				if err == nil {
					fullText += fmt.Sprintf("=== FILE: %s ===\n%s\n", filepath.Base(activePaths[task.FileIndex]), text)
				}
			}
			textArea.SetText(fullText)
			return
		}

		if cmd.OpID == 0 {
			task := cmd.Tasks[0]
			if task.FileIndex >= len(activePaths) {
				return
			}

			pdfBytes, _ := os.ReadFile(activePaths[task.FileIndex])
			wh := WasmHeader{OpID: 0, Pages: task.Pages, PdfSize: uint32(len(pdfBytes))}
			hBytes, _ := json.Marshal(wh)

			bufferSize := uint32(len(pdfBytes)) * 3
			if bufferSize < 20971520 {
				bufferSize = 20971520
			}

			payload := make([]byte, bufferSize)
			binary.LittleEndian.PutUint32(payload[0:4], uint32(len(hBytes)))
			copy(payload[4:4+len(hBytes)], hBytes)
			copy(payload[4+len(hBytes):], pdfBytes)

			res := executeWasmModule("../compiled-binaries/pdf_engine.wasm", payload)

			if len(res) > 0 {
				finalPath := getUniqueFilePath(filepath.Dir(activePaths[task.FileIndex]), "extracted", ".pdf")
				os.WriteFile(finalPath, res, 0644)
				dialog.ShowInformation("Success", "Extracted pages saved to:\n"+finalPath, sandbox)
			}
			return
		}

		if cmd.OpID == 2 {
			var tempFiles []string

			for i, task := range cmd.Tasks {
				if task.FileIndex >= len(activePaths) {
					continue
				}

				if len(task.Pages) > 0 {
					pdfBytes, _ := os.ReadFile(activePaths[task.FileIndex])
					wh := WasmHeader{OpID: 0, Pages: task.Pages, PdfSize: uint32(len(pdfBytes))}
					hBytes, _ := json.Marshal(wh)

					bufferSize := uint32(len(pdfBytes)) * 3
					if bufferSize < 20971520 {
						bufferSize = 20971520
					}

					payload := make([]byte, bufferSize)
					binary.LittleEndian.PutUint32(payload[0:4], uint32(len(hBytes)))
					copy(payload[4:4+len(hBytes)], hBytes)
					copy(payload[4+len(hBytes):], pdfBytes)

					res := executeWasmModule("../compiled-binaries/pdf_engine.wasm", payload)

					if len(res) > 0 {
						tmpName := filepath.Join(os.TempDir(), fmt.Sprintf("host_temp_%d.pdf", i))
						os.WriteFile(tmpName, res, 0644)
						tempFiles = append(tempFiles, tmpName)
					}
				} else {
					tempFiles = append(tempFiles, activePaths[task.FileIndex])
				}
			}

			if len(tempFiles) > 0 {
				finalPath := getUniqueFilePath(filepath.Dir(activePaths[0]), "merged_output", ".pdf")
				err := api.MergeCreateFile(tempFiles, finalPath, false, nil)

				for _, f := range tempFiles {
					if strings.Contains(f, "host_temp_") {
						os.Remove(f)
					}
				}

				if err == nil {
					dialog.ShowInformation("Success", "Merged PDF saved successfully to:\n"+finalPath, sandbox)
				} else {
					dialog.ShowError(err, sandbox)
				}
			}
		}
	})

	workspaceLayout := container.NewBorder(
		container.NewVBox(
			widget.NewLabel("Isolated Document Utility (Hybrid Engine)"),
			commandEntry,
			executeBtn,
		),
		nil, nil, nil,
		textArea,
	)

	pathEntry := widget.NewMultiLineEntry()
	pathEntry.SetPlaceHolder("Paste absolute paths to .pdf files here (One path per line)...")
	loadBtn := widget.NewButton("Mount Workspace", func() {
		lines := strings.Split(pathEntry.Text, "\n")
		activePaths = []string{}
		for _, line := range lines {
			clean := strings.Trim(strings.TrimSpace(line), `"'`)
			if clean != "" {
				activePaths = append(activePaths, clean)
			}
		}

		if len(activePaths) > 0 {
			sandbox.SetContent(workspaceLayout)
		} else {
			dialog.ShowInformation("Error", "Please provide at least one valid path.", sandbox)
		}
	})

	loaderContainer := container.NewBorder(
		widget.NewLabel("Initialize Independent PDF Sandbox\nFor merging, paste multiple paths (one on each line)."),
		loadBtn, nil, nil, pathEntry,
	)
	sandbox.SetContent(loaderContainer)
	sandbox.Show()
}

func spawnArchiveSandbox(fyneApp fyne.App) {
	sandbox := fyneApp.NewWindow("Archive Sandbox")
	sandbox.Resize(fyne.NewSize(600, 300))
	pathEntry := widget.NewEntry()
	pathEntry.SetPlaceHolder("Enter absolute path to file, folder, or .tar.gz...")
	statusLabel := widget.NewLabel("Ready. Provide a path to compress or extract.")
	statusLabel.Wrapping = fyne.TextWrapWord
	compressBtn := widget.NewButton("Compress", func() {
		cleanPath := strings.Trim(strings.TrimSpace(pathEntry.Text), `"'`)
		if cleanPath == "" {
			statusLabel.SetText("Error: Please provide a valid path.")
			return
		}
		statusLabel.SetText("Stitching files into memory buffer...")
		tarBytes, err := createTarArchive(cleanPath)
		if err != nil {
			statusLabel.SetText(fmt.Sprintf("Error reading target: %v", err))
			return
		}
		statusLabel.SetText("Passing buffer to Wasm Engine...")
		actualLen := uint32(len(tarBytes))
		bufferSize := actualLen + 8192
		payload := make([]byte, bufferSize)
		binary.LittleEndian.PutUint32(payload[0:4], actualLen)
		binary.LittleEndian.PutUint32(payload[4:8], 0)
		copy(payload[8:8+actualLen], tarBytes)
		res := executeWasmModule("../compiled-binaries/archive_engine.wasm", payload)
		if len(res) == 0 || len(res) == int(bufferSize) {
			statusLabel.SetText("Engine Error: Compression failed.")
			return
		}
		parentDir := filepath.Dir(cleanPath)
		baseName := filepath.Base(cleanPath)
		finalPath := getUniqueFilePath(parentDir, baseName, ".tar.gz")
		err = os.WriteFile(finalPath, res, 0644)
		if err != nil {
			statusLabel.SetText(fmt.Sprintf("Error writing archive to disk: %v", err))
			return
		}
		statusLabel.SetText(fmt.Sprintf("SUCCESS!\nArchive saved securely at:\n%s", finalPath))
	})
	extractBtn := widget.NewButton("Extract (.tar.gz)", func() {
		cleanPath := strings.Trim(strings.TrimSpace(pathEntry.Text), `"'`)
		if cleanPath == "" || !strings.HasSuffix(cleanPath, ".tar.gz") {
			statusLabel.SetText("Error: Please provide a path to a .tar.gz file.")
			return
		}
		statusLabel.SetText("Reading archive into memory...")
		gzBytes, err := os.ReadFile(cleanPath)
		if err != nil {
			statusLabel.SetText(fmt.Sprintf("Error reading archive: %v", err))
			return
		}
		statusLabel.SetText("Decompressing in Wasm Engine...")
		actualLen := uint32(len(gzBytes))
		bufferSize := actualLen * 20
		if bufferSize < 52428800 {
			bufferSize = 52428800
		}
		payload := make([]byte, bufferSize)
		binary.LittleEndian.PutUint32(payload[0:4], actualLen)
		binary.LittleEndian.PutUint32(payload[4:8], 1)
		copy(payload[8:8+actualLen], gzBytes)
		res := executeWasmModule("../compiled-binaries/archive_engine.wasm", payload)
		if len(res) == 0 || len(res) == int(bufferSize) {
			statusLabel.SetText("Engine Error: Decompression failed.")
			return
		}
		parentDir := filepath.Dir(cleanPath)
		baseName := strings.TrimSuffix(filepath.Base(cleanPath), ".tar.gz")
		destFolder := getUniqueFilePath(parentDir, baseName+"_extracted", "")
		err = extractTarArchive(res, destFolder)
		if err != nil {
			statusLabel.SetText(fmt.Sprintf("Error writing files to disk: %v", err))
			return
		}
		statusLabel.SetText(fmt.Sprintf("SUCCESS!\nFiles safely extracted to:\n%s", destFolder))
	})
	btnGrid := container.NewGridWithColumns(2, compressBtn, extractBtn)
	content := container.NewVBox(widget.NewLabel("Isolated Data Compression Utility"), pathEntry, btnGrid, widget.NewSeparator(), statusLabel)
	sandbox.SetContent(container.NewCenter(content))
	sandbox.Show()
}

func spawnImageSandbox(fyneApp fyne.App, apiKey string) {
	sandbox := fyneApp.NewWindow("Image Sandbox")
	sandbox.Resize(fyne.NewSize(800, 600))
	var activeImage *image.RGBA
	var originalImage *image.RGBA
	var history []*image.RGBA
	var globalWasmTarget string
	var activeFilePath string
	imgCanvas := canvas.NewImageFromImage(nil)
	imgCanvas.FillMode = canvas.ImageFillContain
	undoBtn := widget.NewButton("Undo", func() {
		if len(history) > 0 {
			lastIdx := len(history) - 1
			activeImage = history[lastIdx]
			history = history[:lastIdx]
			imgCanvas.Image = activeImage
			imgCanvas.Refresh()
		}
	})
	resetBtn := widget.NewButton("Reset to Original", func() {
		if originalImage != nil {
			history = nil
			activeImage = cloneImage(originalImage)
			imgCanvas.Image = activeImage
			imgCanvas.Refresh()
		}
	})
	saveBtn := widget.NewButton("Save to Disk", func() {
		if activeImage != nil && activeFilePath != "" {
			f, err := os.Create(activeFilePath)
			if err != nil {
				dialog.ShowError(err, sandbox)
				return
			}
			defer f.Close()
			err = png.Encode(f, activeImage)
			if err != nil {
				dialog.ShowError(err, sandbox)
			} else {
				dialog.ShowInformation("Success", "Image successfully saved to disk.", sandbox)
			}
		}
	})
	topBar := container.NewHBox(undoBtn, resetBtn, saveBtn)
	controlPanel := container.NewVBox()
	slidersRegistry := make(map[string]*widget.Slider)
	var renderUI func(hierarchy []KernelComponent)
	executeAction := func(comp KernelComponent) {
		targetWasm := comp.TargetWasm
		if targetWasm == "" {
			targetWasm = globalWasmTarget
		}
		if targetWasm == "" || activeImage == nil {
			return
		}
		history = append(history, cloneImage(activeImage))
		currentBounds := activeImage.Bounds()
		currentW, currentH := uint32(currentBounds.Dx()), uint32(currentBounds.Dy())
		if strings.Contains(targetWasm, "crop") {
			cX, cY := uint32(0), uint32(0)
			cW, cH := currentW, currentH
			for id, slider := range slidersRegistry {
				lid := strings.ToLower(id)
				if strings.Contains(lid, "x") {
					cX = uint32(slider.Value)
				} else if strings.Contains(lid, "y") {
					cY = uint32(slider.Value)
				} else if strings.Contains(lid, "w") {
					cW = uint32(slider.Value)
				} else if strings.Contains(lid, "h") {
					cH = uint32(slider.Value)
				}
			}
			if cX >= currentW {
				cX = currentW - 1
			}
			if cY >= currentH {
				cY = currentH - 1
			}
			if cX+cW > currentW {
				cW = currentW - cX
			}
			if cY+cH > currentH {
				cH = currentH - cY
			}
			if cW == 0 {
				cW = 1
			}
			if cH == 0 {
				cH = 1
			}
			h := make([]byte, 24)
			binary.LittleEndian.PutUint32(h[0:4], currentW)
			binary.LittleEndian.PutUint32(h[4:8], currentH)
			binary.LittleEndian.PutUint32(h[8:12], cX)
			binary.LittleEndian.PutUint32(h[12:16], cY)
			binary.LittleEndian.PutUint32(h[16:20], cW)
			binary.LittleEndian.PutUint32(h[20:24], cH)
			payload := append(h, activeImage.Pix...)
			res := executeWasmModule(targetWasm, payload)
			newImg := image.NewRGBA(image.Rect(0, 0, int(cW), int(cH)))
			copy(newImg.Pix, res[0:cW*cH*4])
			activeImage = newImg
		} else if strings.Contains(targetWasm, "resize") {
			cW, cH := currentW, currentH
			for id, slider := range slidersRegistry {
				lid := strings.ToLower(id)
				if strings.Contains(lid, "w") {
					cW = uint32(slider.Value)
				} else if strings.Contains(lid, "h") {
					cH = uint32(slider.Value)
				}
			}
			if cW == 0 {
				cW = 1
			}
			if cH == 0 {
				cH = 1
			}
			h := make([]byte, 24)
			binary.LittleEndian.PutUint32(h[0:4], currentW)
			binary.LittleEndian.PutUint32(h[4:8], currentH)
			binary.LittleEndian.PutUint32(h[16:20], cW)
			binary.LittleEndian.PutUint32(h[20:24], cH)
			payload := append(h, activeImage.Pix...)
			req := cW * cH * 4
			if req > uint32(len(activeImage.Pix)) {
				payload = append(payload, make([]byte, req-uint32(len(activeImage.Pix)))...)
			}
			res := executeWasmModule(targetWasm, payload)
			newImg := image.NewRGBA(image.Rect(0, 0, int(cW), int(cH)))
			copy(newImg.Pix, res[0:req])
			activeImage = newImg
		} else if strings.Contains(targetWasm, "brightness") {
			bVal := int32(0)
			for id, slider := range slidersRegistry {
				if strings.Contains(strings.ToLower(id), "bright") || strings.Contains(strings.ToLower(id), "val") {
					bVal = int32(slider.Value)
				}
			}
			h := make([]byte, 24)
			binary.LittleEndian.PutUint32(h[0:4], currentW)
			binary.LittleEndian.PutUint32(h[4:8], currentH)
			binary.LittleEndian.PutUint32(h[8:12], uint32(bVal))
			payload := append(h, activeImage.Pix...)
			res := executeWasmModule(targetWasm, payload)
			copy(activeImage.Pix, res[24:])
		} else {
			res := executeWasmModule(targetWasm, activeImage.Pix)
			copy(activeImage.Pix, res)
		}
		imgCanvas.Image = activeImage
		imgCanvas.Refresh()
	}
	renderUI = func(hierarchy []KernelComponent) {
		controlPanel.RemoveAll()
		slidersRegistry = make(map[string]*widget.Slider)
		globalWasmTarget = ""
		for _, comp := range hierarchy {
			if comp.TargetWasm != "" {
				globalWasmTarget = comp.TargetWasm
			}
		}
		for _, comp := range hierarchy {
			currentComp := comp
			if currentComp.Type == "slider" {
				maxVal, minVal := getFloat(currentComp.Max), getFloat(currentComp.Min)
				idLower := strings.ToLower(currentComp.ID)
				if strings.Contains(globalWasmTarget, "resize") {
					maxVal = 4000
				} else if maxVal <= 0 && activeImage != nil {
					bounds := activeImage.Bounds()
					if strings.Contains(idLower, "w") || strings.Contains(idLower, "x") {
						maxVal = float64(bounds.Dx())
					} else if strings.Contains(idLower, "h") || strings.Contains(idLower, "y") {
						maxVal = float64(bounds.Dy())
					} else {
						maxVal = 255
					}
				}
				if strings.Contains(idLower, "w") || strings.Contains(idLower, "h") {
					if minVal <= 0 {
						minVal = 1
					}
				}
				if strings.Contains(globalWasmTarget, "brightness") {
					minVal = -255
					maxVal = 255
				}
				slider := widget.NewSlider(minVal, maxVal)
				compVal := getFloat(currentComp.Value)
				if compVal > 0 {
					slider.SetValue(compVal)
				} else if activeImage != nil {
					if strings.Contains(idLower, "w") {
						slider.SetValue(float64(activeImage.Bounds().Dx()))
					} else if strings.Contains(idLower, "h") {
						slider.SetValue(float64(activeImage.Bounds().Dy()))
					} else {
						slider.SetValue(minVal)
					}
				}
				slidersRegistry[currentComp.ID] = slider
				label := widget.NewLabel(fmt.Sprintf("%s: %.0f", currentComp.Text, slider.Value))
				slider.OnChanged = func(v float64) { label.SetText(fmt.Sprintf("%s: %.0f", currentComp.Text, v)) }
				controlPanel.Add(container.NewVBox(label, slider))
			} else if currentComp.Type == "button" {
				controlPanel.Add(widget.NewButton(currentComp.Text, func() { executeAction(currentComp) }))
			}
		}
	}
	localSpotlight := widget.NewEntry()
	localSpotlight.SetPlaceHolder("Spotlight: Command the LLM to inject a tool...")
	localSpotlight.OnSubmitted = func(s string) {
		layout := fetchLocalSandboxUI("image", s, apiKey)
		renderUI(layout.Hierarchy)
		localSpotlight.SetText("")
	}
	mainWorkspaceLayout := container.NewBorder(topBar, container.NewVBox(controlPanel, localSpotlight), nil, nil, container.NewMax(imgCanvas))
	pathEntry := widget.NewEntry()
	pathEntry.SetPlaceHolder("Paste absolute path to .png file here...")
	loadBtn := widget.NewButton("Mount Workspace", func() {
		cleanPath := strings.Trim(strings.TrimSpace(pathEntry.Text), `"'`)
		f, err := os.Open(cleanPath)
		if err != nil {
			dialog.ShowError(err, sandbox)
			return
		}
		defer f.Close()
		srcImg, _ := png.Decode(f)
		bounds := srcImg.Bounds()
		rgbaImg := image.NewRGBA(bounds)
		draw.Draw(rgbaImg, bounds, srcImg, bounds.Min, draw.Src)
		activeImage = rgbaImg
		originalImage = cloneImage(activeImage)
		activeFilePath = cleanPath
		imgCanvas.Image = activeImage
		sandbox.SetContent(mainWorkspaceLayout)
	})
	loaderContainer := container.NewVBox(widget.NewLabel("Initialize Independent Image Sandbox"), pathEntry, loadBtn)
	sandbox.SetContent(container.NewCenter(loaderContainer))
	sandbox.Show()
}

func spawnCSVSandbox(fyneApp fyne.App, apiKey string) {
	sandbox := fyneApp.NewWindow("CSV Sandbox")
	sandbox.Resize(fyne.NewSize(1000, 700))
	var activeCSVText string
	var originalCSVText string
	var csvHistory []string
	var globalWasmTarget string
	var gridData [][]string
	var activeFilePath string

	parseCSV := func(text string) {
		gridData = nil
		reader := csv.NewReader(strings.NewReader(text))
		reader.LazyQuotes = true
		reader.FieldsPerRecord = -1
		records, err := reader.ReadAll()
		if err == nil {
			gridData = records
		}
	}

	rebuildCSV := func() string {
		var buf bytes.Buffer
		writer := csv.NewWriter(&buf)
		writer.WriteAll(gridData)
		return buf.String()
	}

	cellEditor := widget.NewMultiLineEntry()
	cellEditor.Wrapping = fyne.TextWrapWord
	cellEditor.SetPlaceHolder("Select a cell in the grid to edit its contents here...")
	var selectedR, selectedC int = -1, -1

	table := widget.NewTable(
		func() (int, int) {
			if len(gridData) == 0 {
				return 0, 0
			}
			return len(gridData), len(gridData[0])
		},
		func() fyne.CanvasObject {
			label := widget.NewLabel(" ")
			label.Truncation = fyne.TextTruncateEllipsis
			return label
		},
		func(id widget.TableCellID, o fyne.CanvasObject) {
			label := o.(*widget.Label)
			if id.Row < len(gridData) && id.Col < len(gridData[id.Row]) {
				cellText := gridData[id.Row][id.Col]
				singleLineText := strings.ReplaceAll(cellText, "\n", " ")
				label.SetText(singleLineText)
			}
		},
	)

	table.OnSelected = func(id widget.TableCellID) {
		if id.Row < len(gridData) && id.Col < len(gridData[id.Row]) {
			selectedR = id.Row
			selectedC = id.Col
			cellEditor.SetText(gridData[id.Row][id.Col])
		}
	}

	parseAndRefreshTable := func() {
		parseCSV(activeCSVText)
		if len(gridData) > 0 {
			for c := 0; c < len(gridData[0]); c++ {
				table.SetColumnWidth(c, 250)
			}
			for r := 0; r < len(gridData); r++ {
				table.SetRowHeight(r, 40)
			}
		}
		table.Refresh()
	}

	applyEditBtn := widget.NewButton("Apply Cell Edit", func() {
		if selectedR >= 0 && selectedC >= 0 {
			csvHistory = append(csvHistory, activeCSVText)
			gridData[selectedR][selectedC] = cellEditor.Text
			activeCSVText = rebuildCSV()
			parseAndRefreshTable()
		}
	})

	editorContainer := container.NewBorder(nil, applyEditBtn, nil, nil, cellEditor)

	undoBtn := widget.NewButton("Undo", func() {
		if len(csvHistory) > 0 {
			lastIdx := len(csvHistory) - 1
			activeCSVText = csvHistory[lastIdx]
			csvHistory = csvHistory[:lastIdx]
			parseAndRefreshTable()
		}
	})

	resetBtn := widget.NewButton("Reset to Original", func() {
		if originalCSVText != "" {
			csvHistory = nil
			activeCSVText = originalCSVText
			parseAndRefreshTable()
		}
	})

	saveBtn := widget.NewButton("Save to Disk", func() {
		if activeCSVText != "" && activeFilePath != "" {
			err := os.WriteFile(activeFilePath, []byte(activeCSVText), 0644)
			if err != nil {
				dialog.ShowError(err, sandbox)
			} else {
				dialog.ShowInformation("Success", "Data successfully saved to disk.", sandbox)
			}
		}
	})

	topBar := container.NewHBox(undoBtn, resetBtn, saveBtn)
	controlPanel := container.NewVBox()
	slidersRegistry := make(map[string]*widget.Slider)
	entriesRegistry := make(map[string]*widget.Entry)
	var renderUI func(hierarchy []KernelComponent)

	executeAction := func(comp KernelComponent) {
		targetWasm := comp.TargetWasm
		if targetWasm == "" {
			targetWasm = globalWasmTarget
		}
		if targetWasm == "" || activeCSVText == "" {
			return
		}

		if strings.Contains(targetWasm, "csv") {
			idLower := strings.ToLower(comp.ID)

			if strings.Contains(idLower, "query") || strings.Contains(idLower, "filter") {
				queryStr := ""
				for _, entry := range entriesRegistry {
					queryStr = entry.Text
					break
				}
				if queryStr == "" {
					return
				}

				qBytes := []byte(queryStr)
				qLen := uint32(len(qBytes))

				h := make([]byte, 8)
				binary.LittleEndian.PutUint32(h[0:4], 3)
				binary.LittleEndian.PutUint32(h[4:8], qLen)

				payload := append(h, qBytes...)
				payload = append(payload, []byte(activeCSVText)...)

				res := executeWasmModule(targetWasm, payload)
				csvHistory = append(csvHistory, activeCSVText)
				activeCSVText = string(res)
				parseAndRefreshTable()
				return
			}

			cIdx := uint32(0)
			for _, slider := range slidersRegistry {
				cIdx = uint32(slider.Value)
				break
			}

			if strings.Contains(idLower, "graph") || strings.Contains(idLower, "chart") || strings.Contains(idLower, "visualize") {
				h := make([]byte, 8)
				binary.LittleEndian.PutUint32(h[0:4], 4)
				binary.LittleEndian.PutUint32(h[4:8], cIdx)
				payload := append(h, []byte(activeCSVText)...)
				res := executeWasmModule(targetWasm, payload)
				var graphPoints []ChartDataPoint
				err := json.Unmarshal(res, &graphPoints)
				if err != nil {
					dialog.ShowError(err, sandbox)
					return
				}
				renderNativeVectorChart(fyneApp, graphPoints)
				return
			}

			opID := uint32(0)
			if strings.Contains(idLower, "sort") {
				opID = 1
			} else if strings.Contains(idLower, "delete") || strings.Contains(idLower, "drop") {
				opID = 2
			}

			h := make([]byte, 8)
			binary.LittleEndian.PutUint32(h[0:4], opID)
			binary.LittleEndian.PutUint32(h[4:8], cIdx)
			payload := append(h, []byte(activeCSVText)...)
			res := executeWasmModule(targetWasm, payload)

			if opID == 0 {
				dialog.ShowInformation("Compute Engine Output", strings.TrimSpace(string(res)), sandbox)
			} else {
				csvHistory = append(csvHistory, activeCSVText)
				activeCSVText = string(res)
				parseAndRefreshTable()
			}
		}
	}

	renderUI = func(hierarchy []KernelComponent) {
		controlPanel.RemoveAll()
		slidersRegistry = make(map[string]*widget.Slider)
		entriesRegistry = make(map[string]*widget.Entry)
		globalWasmTarget = ""
		for _, comp := range hierarchy {
			if comp.TargetWasm != "" {
				globalWasmTarget = comp.TargetWasm
			}
		}

		for _, comp := range hierarchy {
			currentComp := comp
			if currentComp.Type == "slider" {
				maxVal := getFloat(currentComp.Max)
				if maxVal == 0 {
					maxVal = 10
				}
				slider := widget.NewSlider(0, maxVal)
				slider.SetValue(getFloat(currentComp.Value))
				slidersRegistry[currentComp.ID] = slider
				label := widget.NewLabel(fmt.Sprintf("%s: %.0f", currentComp.Text, slider.Value))
				slider.OnChanged = func(v float64) { label.SetText(fmt.Sprintf("%s: %.0f", currentComp.Text, v)) }
				controlPanel.Add(container.NewVBox(label, slider))
			} else if currentComp.Type == "input" {
				entry := widget.NewEntry()
				entry.SetPlaceHolder(currentComp.Placeholder)
				entriesRegistry[currentComp.ID] = entry
				controlPanel.Add(container.NewVBox(widget.NewLabel(currentComp.Text), entry))
			} else if currentComp.Type == "button" {
				controlPanel.Add(widget.NewButton(currentComp.Text, func() { executeAction(currentComp) }))
			}
		}
	}

	localSpotlight := widget.NewEntry()
	localSpotlight.SetPlaceHolder("Spotlight: Command the LLM to inject a data tool...")
	localSpotlight.OnSubmitted = func(s string) {
		layout := fetchLocalSandboxUI("csv", s, apiKey)
		renderUI(layout.Hierarchy)
		localSpotlight.SetText("")
	}

	gridWorkspace := container.NewVSplit(table, editorContainer)
	gridWorkspace.SetOffset(0.75)

	mainWorkspaceLayout := container.NewBorder(topBar, container.NewVBox(controlPanel, localSpotlight), nil, nil, gridWorkspace)
	pathEntry := widget.NewEntry()
	pathEntry.SetPlaceHolder("Paste absolute path to .csv file here...")
	loadBtn := widget.NewButton("Mount Workspace", func() {
		cleanPath := strings.Trim(strings.TrimSpace(pathEntry.Text), `"'`)
		bytes, err := os.ReadFile(cleanPath)
		if err != nil {
			dialog.ShowError(err, sandbox)
			return
		}
		activeCSVText = string(bytes)
		originalCSVText = activeCSVText
		activeFilePath = cleanPath
		csvHistory = nil
		parseAndRefreshTable()
		sandbox.SetContent(mainWorkspaceLayout)
	})
	loaderContainer := container.NewVBox(widget.NewLabel("Initialize Independent Data Sandbox"), pathEntry, loadBtn)
	sandbox.SetContent(container.NewCenter(loaderContainer))
	sandbox.Show()
}

func spawnMarkdownSandbox(fyneApp fyne.App) {
	sandbox := fyneApp.NewWindow("Live Transpiler")
	sandbox.Resize(fyne.NewSize(1000, 600))

	inputEditor := widget.NewMultiLineEntry()
	inputEditor.SetPlaceHolder("Type here...")
	inputEditor.Wrapping = fyne.TextWrapWord

	outputLabel := widget.NewLabel("")
	outputLabel.Wrapping = fyne.TextWrapWord
	outputScroll := container.NewScroll(outputLabel)

	mode := 0 // 0: MD se HTML, 1: HTML to MD
	modeBtn := widget.NewButton("Mode: MD -> HTML", nil)
	modeBtn.OnTapped = func() {
		if mode == 0 {
			mode = 1
			modeBtn.SetText("Mode: HTML -> MD")
		} else {
			mode = 0
			modeBtn.SetText("Mode: MD -> HTML")
		}
	}

	saveBtn := widget.NewButton("Save Output", func() {
		dialog.ShowFileSave(func(writer fyne.URIWriteCloser, err error) {
			if err == nil && writer != nil {
				writer.Write([]byte(outputLabel.Text))
				writer.Close()
			}
		}, sandbox)
	})

	inputEditor.OnChanged = func(s string) {
		if s == "" {
			outputLabel.SetText("")
			return
		}

		inputBytes := []byte(s)

		bufferSize := 8 + len(inputBytes)*5
		if bufferSize < 65536 {
			bufferSize = 65536
		}

		payload := make([]byte, bufferSize)
		binary.LittleEndian.PutUint32(payload[0:4], uint32(mode))
		binary.LittleEndian.PutUint32(payload[4:8], uint32(len(inputBytes)))
		copy(payload[8:], inputBytes)

		res := executeWasmModule("../compiled-binaries/markdown_engine.wasm", payload)

		outputLabel.SetText(string(res))
	}

	split := container.NewHSplit(inputEditor, outputScroll)
	split.SetOffset(0.5)

	sandbox.SetContent(container.NewBorder(
		container.NewHBox(modeBtn, saveBtn),
		nil, nil, nil, split,
	))
	sandbox.Show()
}

func main() {
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		panic("GROQ_API_KEY environment variable is not set")
	}
	generateSampleCSV()
	myApp := app.New()
	daemon := myApp.NewWindow("Master OS Daemon")
	daemon.Resize(fyne.NewSize(500, 80))
	masterSpotlight := widget.NewEntry()
	masterSpotlight.SetPlaceHolder("Spawn Application: What sandbox do you want to open?")
	masterSpotlight.OnSubmitted = func(s string) {
		appType := routeDaemonIntent(s, apiKey)
		if appType == "crypto" {
			spawnCryptoSandbox(myApp)
		} else if appType == "image" {
			spawnImageSandbox(myApp, apiKey)
		} else if appType == "csv" {
			spawnCSVSandbox(myApp, apiKey)
		} else if appType == "archive" {
			spawnArchiveSandbox(myApp)
		} else if appType == "pdf" {
			spawnPDFSandbox(myApp, apiKey)
		} else if appType == "markdown" {
			spawnMarkdownSandbox(myApp)
		} else {
			dialog.ShowInformation("Daemon", "Could not determine Sandbox archetype.", daemon)
		}
		masterSpotlight.SetText("")
	}
	daemon.SetContent(container.NewVBox(masterSpotlight))
	daemon.SetMaster()
	daemon.ShowAndRun()
}