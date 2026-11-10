package tui

import (
	"bytes"
	"io"
	"strconv"
)

// noColorWriter strips SGR color parameters from the trusted renderer output
// while preserving attributes. It is installed only when NO_COLOR is set.
type noColorWriter struct {
	w       io.Writer
	pending []byte
}

func (f *noColorWriter) Write(p []byte) (int, error) {
	data := p
	if len(f.pending) > 0 {
		data = append(f.pending, p...)
		f.pending = nil
	}
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); {
		if data[i] != 0x1b {
			out = append(out, data[i])
			i++
			continue
		}
		if i+1 >= len(data) {
			f.pending = append(f.pending, data[i:]...)
			break
		}
		if data[i+1] != '[' {
			out = append(out, data[i], data[i+1])
			i += 2
			continue
		}
		j := i + 2
		for j < len(data) && (data[j] < 0x40 || data[j] > 0x7e) {
			j++
		}
		if j >= len(data) {
			f.pending = append(f.pending, data[i:]...)
			break
		}
		seq := data[i : j+1]
		if data[j] == 'm' {
			out = append(out, filterSGR(seq)...)
		} else {
			out = append(out, seq...)
		}
		i = j + 1
	}
	if len(out) > 0 {
		if _, err := f.w.Write(out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// filterSGR removes foreground/background/underline-color parameters.
func filterSGR(seq []byte) []byte {
	if len(seq) < 3 || seq[0] != 0x1b || seq[1] != '[' || seq[len(seq)-1] != 'm' {
		return seq
	}
	params := string(seq[2 : len(seq)-1])
	if params == "" {
		return seq
	}
	parts := bytes.Split(seq[2:len(seq)-1], []byte(";"))
	kept := make([][]byte, 0, len(parts))
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		if bytes.ContainsRune(part, ':') {
			if bytes.HasPrefix(part, []byte("38")) || bytes.HasPrefix(part, []byte("48")) || bytes.HasPrefix(part, []byte("58")) {
				continue
			}
			kept = append(kept, part)
			continue
		}
		n, err := strconv.Atoi(string(part))
		if err != nil {
			kept = append(kept, part)
			continue
		}
		if n == 38 || n == 48 || n == 58 {
			if i+1 < len(parts) && string(parts[i+1]) == "5" {
				i += 2
			} else if i+1 < len(parts) && string(parts[i+1]) == "2" {
				i += 4
			} else {
				i++
			}
			continue
		}
		if (n >= 30 && n <= 37) || (n >= 90 && n <= 97) ||
			(n >= 40 && n <= 47) || (n >= 100 && n <= 107) || n == 39 || n == 49 {
			continue
		}
		kept = append(kept, part)
	}
	if len(kept) == 0 {
		return nil
	}
	var b bytes.Buffer
	b.WriteString("\x1b[")
	for i, part := range kept {
		if i > 0 {
			b.WriteByte(';')
		}
		b.Write(part)
	}
	b.WriteByte('m')
	return b.Bytes()
}
