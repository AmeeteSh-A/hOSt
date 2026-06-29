package main

type Component struct {
	Type        string            `json:"type"`
	ID          string            `json:"id"`
	Text        string            `json:"text,omitempty"`
	Placeholder string            `json:"placeholder,omitempty"`
	Action      string            `json:"action,omitempty"`
	TargetWasm  string            `json:"target_wasm,omitempty"`
	Children    []Component       `json:"children,omitempty"`
	Props       map[string]string `json:"props,omitempty"`
}

type Layout struct {
	Title     string      `json:"title"`
	Width     int         `json:"width"`
	Height    int         `json:"height"`
	RootType  string      `json:"root_type"`
	Hierarchy []Component `json:"hierarchy"`
}