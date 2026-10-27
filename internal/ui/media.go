package ui

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gioui.org/op/paint"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/brunoenten/omawhats/internal/wa"
)

const (
	maxDecodedSide = 720
	maxDecoded     = 48
	maxDownloads   = 4
)

type imgState int

const (
	imgLoading imgState = iota
	imgReady
	imgFailed
)

// imgEntry is an inline photo. While the full image loads (or if it fails),
// op holds the blurry thumbnail embedded in the message, if any.
type imgEntry struct {
	state imgState
	op    paint.ImageOp
	hasOp bool
	used  time.Time
}

// images downloads, decodes and caches inline photos.
type images struct {
	mu         sync.Mutex
	m          map[string]*imgEntry
	sem        chan struct{}
	dir        string
	invalidate func()
}

func newImages(dir string, invalidate func()) *images {
	return &images{
		m:          map[string]*imgEntry{},
		sem:        make(chan struct{}, maxDownloads),
		dir:        dir,
		invalidate: invalidate,
	}
}

func (im *images) path(p *wa.Photo) string {
	h := fnv.New64a()
	h.Write([]byte(p.ID))
	return filepath.Join(im.dir, fmt.Sprintf("%016x.jpg", h.Sum64()))
}

// fetch returns the cached full-size file for a photo, downloading it if needed.
func (im *images) fetch(c *wa.Client, p *wa.Photo) (string, error) {
	path := im.path(p)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(im.dir, 0o700); err != nil {
		return "", err
	}
	im.sem <- struct{}{}
	defer func() { <-im.sem }()
	return path, c.DownloadPhoto(*p, path)
}

// get returns a snapshot of the inline image for p, starting a load on first use.
func (im *images) get(c *wa.Client, p *wa.Photo) imgEntry {
	key := p.ID
	im.mu.Lock()
	defer im.mu.Unlock()
	if e, ok := im.m[key]; ok {
		e.used = time.Now()
		return *e
	}
	e := &imgEntry{state: imgLoading, used: time.Now()}
	if thumb, _, err := image.Decode(bytes.NewReader(p.Thumb)); err == nil {
		e.op, e.hasOp = paint.NewImageOp(thumb), true
	}
	im.m[key] = e
	im.evictLocked()
	go func() {
		img, err := im.load(c, p)
		im.mu.Lock()
		if err != nil {
			e.state = imgFailed
		} else {
			e.op, e.hasOp, e.state = paint.NewImageOp(img), true, imgReady
		}
		im.mu.Unlock()
		im.invalidate()
	}()
	return *e
}

func (im *images) load(c *wa.Client, p *wa.Photo) (image.Image, error) {
	path, err := im.fetch(c, p)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	long := max(b.Dx(), b.Dy())
	if long <= maxDecodedSide {
		return src, nil
	}
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx()*maxDecodedSide/long, b.Dy()*maxDecodedSide/long))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst, nil
}

func (im *images) evictLocked() {
	for len(im.m) > maxDecoded {
		var oldest string
		var at time.Time
		for k, e := range im.m {
			if e.state != imgLoading && (oldest == "" || e.used.Before(at)) {
				oldest, at = k, e.used
			}
		}
		if oldest == "" {
			return
		}
		delete(im.m, oldest)
	}
}

// open downloads the full-size photo and hands it to the default viewer.
func (im *images) open(c *wa.Client, p *wa.Photo, status func(string)) {
	status("opening photo…")
	go func() {
		path, err := im.fetch(c, p)
		if err != nil {
			status("photo: " + err.Error())
			return
		}
		xdgOpen(path, status)
	}()
}

// files downloads and caches non-image attachments.
type files struct {
	mu         sync.Mutex
	sem        chan struct{}
	dir        string
	invalidate func()
}

func newFiles(dir string, invalidate func()) *files {
	return &files{
		sem:        make(chan struct{}, maxDownloads),
		dir:        dir,
		invalidate: invalidate,
	}
}

func (fs *files) path(f *wa.File) string {
	h := fnv.New64a()
	h.Write([]byte(f.ID))
	name := sanitizeName(f.Name)
	return filepath.Join(fs.dir, fmt.Sprintf("%016x_%s", h.Sum64(), name))
}

func sanitizeName(name string) string {
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		switch {
		case r < 32, strings.ContainsRune(`<>:"/\|?*`, r):
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	return name
}

func (fs *files) fetch(c *wa.Client, f *wa.File) (string, error) {
	path := fs.path(f)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(fs.dir, 0o700); err != nil {
		return "", err
	}
	fs.sem <- struct{}{}
	defer func() { <-fs.sem }()
	return path, c.DownloadFile(*f, path)
}

func (fs *files) open(c *wa.Client, f *wa.File, status func(string)) {
	status("opening " + f.Label + "…")
	go func() {
		path, err := fs.fetch(c, f)
		if err != nil {
			status("file: " + err.Error())
			return
		}
		xdgOpen(path, status)
	}()
}

func xdgOpen(path string, status func(string)) {
	cmd := exec.Command("xdg-open", path)
	if err := cmd.Start(); err != nil {
		status("xdg-open: " + err.Error())
		return
	}
	go cmd.Wait()
}
