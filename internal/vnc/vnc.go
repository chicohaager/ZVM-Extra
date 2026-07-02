package vnc

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
)

type GraphicsConfig struct {
	Type     string `xml:"type,attr" json:"type"`
	Port     int    `xml:"port,attr" json:"port"`
	Autoport string `xml:"autoport,attr" json:"autoport"`
	Listen   string `xml:"listen,attr" json:"listen"`
	Passwd   string `xml:"passwd,attr,omitempty" json:"passwd,omitempty"`
}

type DomainXML struct {
	Graphics []GraphicsConfig `xml:"devices>graphics"`
}

// GetVNCConfig parses domain XML to find VNC graphics settings.
func GetVNCConfig(xmlStr string) (GraphicsConfig, bool) {
	var doc DomainXML
	if err := xml.Unmarshal([]byte(xmlStr), &doc); err != nil {
		return GraphicsConfig{}, false
	}
	for _, g := range doc.Graphics {
		if g.Type == "vnc" {
			return g, true
		}
	}
	return GraphicsConfig{}, false
}

// ModifyGraphicsXML modifies the XML of a domain to set the VNC graphics configuration.
// If type="vnc" graphics element is found, it is replaced. If not, it is added inside <devices>.
func ModifyGraphicsXML(xmlStr string, port int, listenAddr string, password string) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader([]byte(xmlStr)))
	var buf bytes.Buffer
	encoder := xml.NewEncoder(&buf)

	var foundGraphics bool
	var skipDepth int

	for {
		t, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		if skipDepth > 0 {
			switch t.(type) {
			case xml.StartElement:
				skipDepth++
			case xml.EndElement:
				skipDepth--
			}
			continue
		}

		switch tok := t.(type) {
		case xml.StartElement:
			if tok.Name.Local == "graphics" {
				isVNC := false
				for _, attr := range tok.Attr {
					if attr.Name.Local == "type" && attr.Value == "vnc" {
						isVNC = true
						break
					}
				}
				if isVNC {
					foundGraphics = true
					skipDepth = 1
					if err := writeVNCToken(encoder, port, listenAddr, password); err != nil {
						return "", err
					}
					continue
				}
			}
		case xml.EndElement:
			if tok.Name.Local == "devices" {
				if !foundGraphics {
					// Insert VNC graphics right before closing </devices>
					if err := writeVNCToken(encoder, port, listenAddr, password); err != nil {
						return "", err
					}
					foundGraphics = true
				}
			}
		}

		if err := encoder.EncodeToken(t); err != nil {
			return "", err
		}
	}

	if err := encoder.Flush(); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func writeVNCToken(enc *xml.Encoder, port int, listenAddr string, password string) error {
	autoport := "no"
	portStr := fmt.Sprintf("%d", port)
	if port <= 0 {
		autoport = "yes"
		portStr = "-1"
	}
	if listenAddr == "" {
		listenAddr = "127.0.0.1"
	}

	attrs := []xml.Attr{
		{Name: xml.Name{Local: "type"}, Value: "vnc"},
		{Name: xml.Name{Local: "port"}, Value: portStr},
		{Name: xml.Name{Local: "autoport"}, Value: autoport},
		{Name: xml.Name{Local: "listen"}, Value: listenAddr},
	}
	if password != "" {
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "passwd"}, Value: password})
	}

	start := xml.StartElement{
		Name: xml.Name{Local: "graphics"},
		Attr: attrs,
	}
	if err := enc.EncodeToken(start); err != nil {
		return err
	}

	listenStart := xml.StartElement{
		Name: xml.Name{Local: "listen"},
		Attr: []xml.Attr{
			{Name: xml.Name{Local: "type"}, Value: "address"},
			{Name: xml.Name{Local: "address"}, Value: listenAddr},
		},
	}
	if err := enc.EncodeToken(listenStart); err != nil {
		return err
	}
	if err := enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "listen"}}); err != nil {
		return err
	}

	if err := enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "graphics"}}); err != nil {
		return err
	}

	return nil
}
