package tools

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// webContentKind is the dispatch class for one response body.
type webContentKind int

const (
	webContentUnsupported webContentKind = iota
	webContentHTML
	webContentText
)

// classifyWebContent maps a response media type to a dispatch class. Missing
// or unrecognized types sniff up to 512 bytes with http.DetectContentType.
func classifyWebContent(contentType string, raw []byte) webContentKind {
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil && mediaType != "" {
		if kind, ok := explicitWebContentKind(strings.ToLower(mediaType)); ok {
			return kind
		}
	}
	sniffed, _, err := mime.ParseMediaType(http.DetectContentType(raw[:min(len(raw), maxWebSniffBytes)]))
	if err != nil {
		return webContentUnsupported
	}
	switch strings.ToLower(sniffed) {
	case "text/html":
		return webContentHTML
	case "text/plain", "text/xml":
		return webContentText
	default:
		return webContentUnsupported
	}
}

// explicitWebContentKind reports the dispatch class for a recognized media
// type. ok is false when the type is unrecognized and should be sniffed.
func explicitWebContentKind(mediaType string) (kind webContentKind, ok bool) {
	switch mediaType {
	case "text/html", "application/xhtml+xml":
		return webContentHTML, true
	case "text/plain", "text/markdown", "application/json", "application/xml", "text/xml", "text/csv":
		return webContentText, true
	case "application/pdf", "application/octet-stream":
		return webContentUnsupported, true
	}
	if prefix, _, found := strings.Cut(mediaType, "/"); found {
		switch prefix {
		case "image", "audio", "video":
			return webContentUnsupported, true
		}
	}
	return webContentUnsupported, false
}

// decodeWebBody decodes the declared charset to UTF-8. A Content-Type charset
// and an HTML meta charset are honored, a BOM is honored, and everything else
// defaults to UTF-8 with invalid sequences replaced.
func decodeWebBody(raw []byte, contentType string) (string, error) {
	label := ""
	if mediaType, params, err := mime.ParseMediaType(contentType); err == nil {
		label = params["charset"]
		if label == "" && (mediaType == "text/html" || mediaType == "application/xhtml+xml") {
			label = declaredMetaCharset(raw)
		}
	} else {
		label = declaredMetaCharset(raw)
	}
	if label == "" {
		if _, name, certain := charset.DetermineEncoding(raw, ""); certain {
			label = name
		}
	}
	if label == "" {
		return sanitizeUTF8(string(raw)), nil
	}
	reader, err := charset.NewReaderLabel(label, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return sanitizeUTF8(string(decoded)), nil
}

// declaredMetaCharset scans the first KiB for an HTML meta charset declaration.
func declaredMetaCharset(raw []byte) string {
	prefix := raw[:min(len(raw), 1024)]
	tokenizer := html.NewTokenizer(bytes.NewReader(prefix))
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			return ""
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := tokenizer.TagName()
			if !hasAttr || !bytes.EqualFold(name, []byte("meta")) {
				continue
			}
			charsetLabel := ""
			httpEquiv := ""
			content := ""
			for {
				key, value, more := tokenizer.TagAttr()
				switch strings.ToLower(string(key)) {
				case "charset":
					charsetLabel = string(value)
				case "http-equiv":
					httpEquiv = string(value)
				case "content":
					content = string(value)
				}
				if !more {
					break
				}
			}
			if charsetLabel != "" {
				return charsetLabel
			}
			if strings.EqualFold(httpEquiv, "content-type") {
				if _, params, err := mime.ParseMediaType(content); err == nil {
					if label := params["charset"]; label != "" {
						return label
					}
				}
			}
		}
	}
}
