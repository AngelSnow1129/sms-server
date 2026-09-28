package main

type Mode string

const (
	ModeGo     Mode = "go"
	ModeTinygo Mode = "tinygo"
)

func (m Mode) IsValid() bool {
	switch m {
	case ModeGo, ModeTinygo:
		return true
	}
	return false
}
