package mailin

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits on what a message may make the program do.
const (
	MinAttachment = 8 << 10 // a part smaller than this is not a photo (an icon in a signature); it is dropped
	maxDepth      = 4       // multipart inside multipart
	maxParts      = 40      // parts looked at in one message
	maxFiles      = 40      // files kept from one message
)

// Attachment is a file taken from a message.
type Attachment struct {
	Name string
	Data []byte
}

// Parsed is what a message amounts to: which box it is for, who sent it, and the files in it that might be photos or
// videos. Whether they are is decided by whoever stores them, from their first bytes, not from what the message says.
type Parsed struct {
	Code  string // the box's code, from the address it was sent to; empty when it was not sent to one of ours
	From  string // the sender's address, lower case, for the one-way mark only
	Files []Attachment
}

var codeRE = regexp.MustCompile(`^[a-z0-9]{6,32}$`)

// CodeOf finds the box code in the addresses a message was delivered to: the mailbox's own address with a plus sign and a
// code after it. The headers a mail server adds for the address it delivered to come first (a person's Bcc is not in To).
func CodeOf(h mail.Header, address string) string {
	baseLocal, baseDomain, _ := strings.Cut(strings.ToLower(address), "@")
	for _, name := range []string{"Delivered-To", "X-Original-To", "Envelope-To", "X-Envelope-To", "To", "Cc"} {
		for _, v := range h[textproto.CanonicalMIMEHeaderKey(name)] {
			list, err := mail.ParseAddressList(v)
			if err != nil {
				if one, err := mail.ParseAddress(v); err == nil {
					list = []*mail.Address{one}
				}
			}
			for _, a := range list {
				at := strings.LastIndex(a.Address, "@")
				if at < 0 || !strings.EqualFold(a.Address[at+1:], baseDomain) {
					continue
				}
				local := strings.ToLower(a.Address[:at])
				if rest, ok := strings.CutPrefix(local, baseLocal+"+"); ok && codeRE.MatchString(rest) {
					return rest
				}
			}
		}
	}
	return ""
}

// Parse reads a message. A message that cannot be read at all is an error; one with nothing in it for us is not.
// maxBytes bounds how much of the decoded files is kept.
func Parse(raw []byte, address string, maxBytes int64) (Parsed, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return Parsed{}, err
	}
	p := Parsed{Code: CodeOf(msg.Header, address)}
	if from, err := mail.ParseAddress(msg.Header.Get("From")); err == nil {
		p.From = strings.ToLower(from.Address)
	}
	if p.Code == "" {
		return p, nil // not for one of our boxes: nothing is decoded
	}
	st := &walker{left: maxBytes}
	st.walk(textproto.MIMEHeader(msg.Header), msg.Body, "", 0)
	p.Files = st.files
	return p, nil
}

type walker struct {
	files []Attachment
	parts int
	left  int64
}

func (w *walker) walk(h textproto.MIMEHeader, body io.Reader, filename string, depth int) {
	if w.parts >= maxParts || len(w.files) >= maxFiles || w.left <= 0 {
		return
	}
	w.parts++
	typ, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil {
		typ = "text/plain" // no type: a plain message, which holds no photo
	}
	if strings.HasPrefix(typ, "multipart/") {
		if depth >= maxDepth || params["boundary"] == "" {
			return
		}
		mr := multipart.NewReader(body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err != nil {
				return
			}
			w.walk(part.Header, part, part.FileName(), depth+1)
			if w.parts >= maxParts || len(w.files) >= maxFiles || w.left <= 0 {
				return
			}
		}
	}
	if strings.HasPrefix(typ, "text/") || strings.HasPrefix(typ, "message/") {
		return
	}
	if filename == "" {
		if _, dp, err := mime.ParseMediaType(h.Get("Content-Disposition")); err == nil {
			filename = dp["filename"]
		}
	}
	if filename == "" {
		filename = params["name"]
	}
	r := decode(h.Get("Content-Transfer-Encoding"), body)
	data, err := io.ReadAll(io.LimitReader(r, w.left+1))
	if err != nil || int64(len(data)) > w.left {
		w.left = 0 // too much in all: nothing more is taken
		return
	}
	if len(data) < MinAttachment {
		return
	}
	w.left -= int64(len(data))
	w.files = append(w.files, Attachment{Name: cleanName(filename), Data: data})
}

// decode undoes the transfer encoding of a part. A body that is damaged gives what could be read, which the storing side
// will refuse if it is not a picture.
func decode(encoding string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, &stripper{r: r})
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	}
	return r
}

// stripper removes the line breaks and spaces MIME puts in base64.
type stripper struct{ r io.Reader }

func (s *stripper) Read(p []byte) (int, error) {
	for {
		n, err := s.r.Read(p)
		k := 0
		for _, b := range p[:n] {
			if b != '\r' && b != '\n' && b != ' ' && b != '\t' {
				p[k] = b
				k++
			}
		}
		if k > 0 || err != nil {
			return k, err
		}
	}
}

// cleanName makes a file name safe to show and to store: no path, no control characters, at most 100 characters.
func cleanName(name string) string {
	if dec, err := (&mime.WordDecoder{}).DecodeHeader(name); err == nil {
		name = dec
	}
	name = path.Base(strings.NewReplacer(`\`, `/`).Replace(name))
	var b strings.Builder
	for _, r := range name {
		if unicode.IsControl(r) || r == utf8.RuneError {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if r := []rune(out); len(r) > 100 {
		out = string(r[:100])
	}
	if out == "" || out == "." || out == "/" {
		return "mail-attachment"
	}
	return out
}
