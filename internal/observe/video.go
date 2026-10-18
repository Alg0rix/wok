package observe

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// videoFPS is the constant output rate. The screencast emits frames only when
// the page changes, so the last frame is repeated to keep wall-clock timing.
const videoFPS = 10

type Chapter struct {
	Title string `json:"title"`
	AtMs  int64  `json:"atMs"`
}

// video accumulates JPEG screencast frames into a temporary movi chunk list,
// then writes a Motion-JPEG AVI (transcoded with ffmpeg for other extensions).
type video struct {
	path, session string
	started       time.Time
	tmp           *os.File
	buf           *bufio.Writer
	sizes         []uint32
	last          []byte
	written       int
	width, height int
	chapters      []Chapter
	err           error
}

func newVideo(path, session string) (*video, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".avi" {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			return nil, fmt.Errorf("%s video requires ffmpeg on PATH; use a .avi path for built-in Motion-JPEG", ext)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".wok-video-*")
	if err != nil {
		return nil, err
	}
	return &video{path: path, session: session, started: time.Now(), tmp: tmp, buf: bufio.NewWriter(tmp), chapters: []Chapter{}}, nil
}

// frame repeats the previous frame up to now, then queues the new one.
func (v *video) frame(data []byte, now time.Time) {
	if v.err != nil {
		return
	}
	if v.width == 0 {
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return
		}
		v.width, v.height = cfg.Width, cfg.Height
	}
	v.fill(now)
	v.last = data
	if v.written == 0 {
		v.write(data)
	}
}
func (v *video) fill(now time.Time) {
	due := int(now.Sub(v.started) * videoFPS / time.Second)
	for v.last != nil && v.written < due && v.err == nil {
		v.write(v.last)
	}
}
func (v *video) write(data []byte) {
	pad := len(data) & 1
	header := make([]byte, 8)
	copy(header, "00dc")
	binary.LittleEndian.PutUint32(header[4:], uint32(len(data)))
	if _, v.err = v.buf.Write(header); v.err == nil {
		_, v.err = v.buf.Write(append(data, make([]byte, pad)...))
	}
	v.sizes = append(v.sizes, uint32(len(data)))
	v.written++
}
func (v *video) abort() {
	v.tmp.Close()
	os.Remove(v.tmp.Name())
}

func (v *video) finish() (map[string]any, error) {
	defer v.abort()
	v.fill(time.Now())
	if v.err == nil {
		v.err = v.buf.Flush()
	}
	if v.err != nil {
		return nil, v.err
	}
	if v.written == 0 {
		return nil, errors.New("no frames were captured")
	}
	avi := v.path
	if strings.ToLower(filepath.Ext(v.path)) != ".avi" {
		avi = v.tmp.Name() + ".avi"
		defer os.Remove(avi)
	}
	if err := v.writeAVI(avi); err != nil {
		return nil, err
	}
	if avi != v.path {
		out, err := exec.Command("ffmpeg", "-y", "-loglevel", "error", "-i", avi, v.path).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("ffmpeg: %v: %s", err, out)
		}
	}
	result := map[string]any{"path": v.path, "frames": v.written, "fps": videoFPS, "durationMs": v.written * 1000 / videoFPS, "width": v.width, "height": v.height, "chapters": v.chapters}
	if len(v.chapters) > 0 {
		data, _ := json.MarshalIndent(v.chapters, "", "  ")
		chapters := strings.TrimSuffix(v.path, filepath.Ext(v.path)) + ".chapters.json"
		if err := os.WriteFile(chapters, data, 0600); err != nil {
			return nil, err
		}
		result["chaptersPath"] = chapters
	}
	return result, nil
}

func (v *video) writeAVI(path string) error {
	movi, err := os.Open(v.tmp.Name())
	if err != nil {
		return err
	}
	defer movi.Close()
	info, err := movi.Stat()
	if err != nil {
		return err
	}
	moviSize := uint32(info.Size())
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	w := bufio.NewWriter(out)
	le := func(values ...uint32) {
		for _, x := range values {
			_ = binary.Write(w, binary.LittleEndian, x)
		}
	}
	fourcc := func(s string) { _, _ = w.WriteString(s) }
	frames := uint32(v.written)
	width, height := uint32(v.width), uint32(v.height)
	maxSize := uint32(0)
	for _, s := range v.sizes {
		maxSize = max(maxSize, s)
	}
	const avihSize, strhSize, strfSize = 56, 56, 40
	strlSize := 4 + 8 + strhSize + 8 + strfSize
	hdrlSize := 4 + 8 + avihSize + 8 + uint32(strlSize)
	idxSize := 16 * frames
	riffSize := 4 + 8 + hdrlSize + 8 + 4 + moviSize + 8 + idxSize

	fourcc("RIFF")
	le(riffSize)
	fourcc("AVI ")
	fourcc("LIST")
	le(hdrlSize)
	fourcc("hdrl")
	fourcc("avih")
	le(avihSize, 1000000/videoFPS, maxSize*videoFPS, 0, 0x10, frames, 0, 1, maxSize, width, height, 0, 0, 0, 0)
	fourcc("LIST")
	le(uint32(strlSize))
	fourcc("strl")
	fourcc("strh")
	le(strhSize)
	fourcc("vids")
	fourcc("MJPG")
	le(0, 0, 0, 1, videoFPS, 0, frames, maxSize, 0xFFFFFFFF, 0)
	_ = binary.Write(w, binary.LittleEndian, [4]uint16{0, 0, uint16(width), uint16(height)})
	fourcc("strf")
	le(strfSize, strfSize, width, height)
	_ = binary.Write(w, binary.LittleEndian, [2]uint16{1, 24})
	fourcc("MJPG")
	le(width*height*3, 0, 0, 0, 0)
	fourcc("LIST")
	le(4 + moviSize)
	fourcc("movi")
	if _, err = io.Copy(w, movi); err != nil {
		return err
	}
	fourcc("idx1")
	le(idxSize)
	offset := uint32(4)
	for _, s := range v.sizes {
		fourcc("00dc")
		le(0x10, offset, s)
		offset += 8 + s + s&1
	}
	if err = w.Flush(); err != nil {
		return err
	}
	return out.Sync()
}
