package wa

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"google.golang.org/protobuf/proto"
)

const maxUploadBytes = 100 << 20 // 100 MiB

// SendFile uploads path and sends it to k. Images (jpeg/png/webp) go as
// photos; everything else as a document. caption is optional; replyTo quotes
// an existing message when set.
func (c *Client) SendFile(k ChatKey, path, caption, replyTo string) {
	cli, ok := c.connected()
	if !ok {
		c.Store.SetStatus("send: " + errOffline.Error())
		return
	}
	caption = strings.TrimSpace(caption)
	name := filepath.Base(path)
	c.Store.SetStatus("uploading " + name + "…")

	go func() {
		data, err := readUpload(path)
		if err != nil {
			c.Store.SetStatus("send: " + err.Error())
			return
		}
		mime := http.DetectContentType(data)
		ctx := context.WithoutCancel(c.ctx)
		var (
			msg *waE2E.Message
			out Message
		)
		switch mime {
		case "image/jpeg", "image/png", "image/webp":
			msg, out, err = c.buildImage(ctx, data, mime, caption, name)
		default:
			msg, out, err = c.buildDocument(ctx, data, mime, caption, name)
		}
		if err != nil {
			c.Store.SetStatus("send: " + err.Error())
			return
		}
		if replyTo != "" {
			attachReply(msg, k, replyTo, c)
		}
		resp, err := cli.SendMessage(ctx, k.jid(), msg)
		if err != nil {
			c.Store.SetStatus("send: " + err.Error())
			return
		}
		self := c.self()
		out.ID, out.Peer, out.Out = resp.ID, k, true
		out.Date, out.ReplyTo = resp.Timestamp, replyTo
		out.SenderID, out.senderJID = self.String(), self.String()
		if err := ensureChat(c.db, k); err == nil {
			if inserted, err := insertMessage(c.db, out); err == nil && inserted {
				_ = bumpChat(c.db, k, out.Date.Unix(), preview(out))
			}
		}
		c.Store.SetStatus("sent " + name)
		c.refreshChat(k)
		c.refreshMessage(k, out.ID)
	}()
}

func readUpload(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("is a directory")
	}
	if st.Size() > maxUploadBytes {
		return nil, fmt.Errorf("file too large (max %d MiB)", maxUploadBytes>>20)
	}
	return os.ReadFile(path)
}

func (c *Client) buildImage(ctx context.Context, data []byte, mime, caption, name string) (*waE2E.Message, Message, error) {
	cli, _ := c.connected()
	up, err := cli.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return nil, Message{}, err
	}
	thumb, w, h := imageThumb(data)
	img := &waE2E.ImageMessage{
		URL:           proto.String(up.URL),
		DirectPath:    proto.String(up.DirectPath),
		MediaKey:      up.MediaKey,
		Mimetype:      proto.String(mime),
		FileEncSHA256: up.FileEncSHA256,
		FileSHA256:    up.FileSHA256,
		FileLength:    proto.Uint64(up.FileLength),
		Width:         proto.Uint32(uint32(w)),
		Height:        proto.Uint32(uint32(h)),
		JPEGThumbnail: thumb,
	}
	if caption != "" {
		img.Caption = proto.String(caption)
	}
	msg := &waE2E.Message{ImageMessage: img}
	media, _ := proto.Marshal(img)
	out := Message{
		Text:  caption,
		Photo: &Photo{W: w, H: h, Thumb: thumb, media: media},
	}
	_ = name
	return msg, out, nil
}

func (c *Client) buildDocument(ctx context.Context, data []byte, mime, caption, name string) (*waE2E.Message, Message, error) {
	cli, _ := c.connected()
	up, err := cli.Upload(ctx, data, whatsmeow.MediaDocument)
	if err != nil {
		return nil, Message{}, err
	}
	if mime == "application/octet-stream" {
		if ext := strings.ToLower(filepath.Ext(name)); ext != "" {
			mime = mimeFromExt(ext)
		}
	}
	doc := &waE2E.DocumentMessage{
		URL:           proto.String(up.URL),
		DirectPath:    proto.String(up.DirectPath),
		MediaKey:      up.MediaKey,
		Mimetype:      proto.String(mime),
		FileEncSHA256: up.FileEncSHA256,
		FileSHA256:    up.FileSHA256,
		FileLength:    proto.Uint64(up.FileLength),
		FileName:      proto.String(name),
		Title:         proto.String(name),
	}
	if caption != "" {
		doc.Caption = proto.String(caption)
	}
	media, _ := proto.Marshal(doc)
	label := "[file " + name + "]"
	out := Message{
		Text: caption,
		File: &File{Kind: "document", Name: name, Mime: mime, Label: label, media: media},
	}
	msg := &waE2E.Message{DocumentMessage: doc}
	if caption != "" {
		// WhatsApp expects captions on documents wrapped this way.
		msg = &waE2E.Message{DocumentWithCaptionMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{DocumentMessage: doc},
		}}
	}
	return msg, out, nil
}

func attachReply(msg *waE2E.Message, k ChatKey, replyTo string, c *Client) {
	ci := &waE2E.ContextInfo{StanzaID: proto.String(replyTo)}
	if r, ok := c.Store.Message(k, replyTo); ok {
		ci.Participant = proto.String(r.senderJID)
		ci.QuotedMessage = &waE2E.Message{Conversation: proto.String(r.Summary())}
	}
	switch {
	case msg.GetImageMessage() != nil:
		msg.GetImageMessage().ContextInfo = ci
	case msg.GetDocumentMessage() != nil:
		msg.GetDocumentMessage().ContextInfo = ci
	case msg.GetDocumentWithCaptionMessage().GetMessage().GetDocumentMessage() != nil:
		msg.GetDocumentWithCaptionMessage().GetMessage().GetDocumentMessage().ContextInfo = ci
	}
}

func imageThumb(data []byte) (thumb []byte, w, h int) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0
	}
	b := src.Bounds()
	w, h = b.Dx(), b.Dy()
	const maxSide = 72
	tw, th := w, h
	if long := max(tw, th); long > maxSide {
		tw, th = tw*maxSide/long, th*maxSide/long
	}
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 70}); err != nil {
		return nil, w, h
	}
	return buf.Bytes(), w, h
}

func mimeFromExt(ext string) string {
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".zip":
		return "application/zip"
	case ".txt":
		return "text/plain"
	case ".csv":
		return "text/csv"
	case ".json":
		return "application/json"
	case ".mp4":
		return "video/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".ogg":
		return "audio/ogg"
	case ".opus":
		return "audio/ogg"
	case ".doc":
		return "application/msword"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".ppt":
		return "application/vnd.ms-powerpoint"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	}
	return "application/octet-stream"
}
