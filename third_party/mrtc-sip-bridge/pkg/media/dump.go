package media

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// dumpLimit is the number of PCM samples captured per direction (60 s at 8 kHz).
const dumpLimit = 60 * SampleRate

// dumpSink writes the call's PCM to WAV files when JMPCHAT_MEDIA_DUMP names a directory. It exists
// to analyse audio quality offline: s2r is what the bridge encodes for the Matrix side, r2s is what
// it sends to the PSTN side. Each direction has a single producer and the files are written from
// Stop after the media goroutines have finished.
type dumpSink struct {
	dir string
	mu  sync.Mutex
	s2r []int16
	r2s []int16
}

func newDumpSink(dir string) *dumpSink {
	if dir == "" {
		return nil
	}
	// Best effort: without the directory the captures would be silently dropped.
	_ = os.MkdirAll(dir, 0o777)
	return &dumpSink{dir: dir}
}

func (d *dumpSink) addS2R(pcm []int16) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if len(d.s2r) < dumpLimit {
		d.s2r = append(d.s2r, pcm...)
	}
	d.mu.Unlock()
}

func (d *dumpSink) addR2S(ulaw []byte) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if len(d.r2s) < dumpLimit {
		var pcm [FrameSamples]int16
		ULawDecode(pcm[:], ulaw)
		d.r2s = append(d.r2s, pcm[:]...)
	}
	d.mu.Unlock()
}

// flush writes the captured audio (called once, after the media goroutines stopped).
func (d *dumpSink) flush(tag string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	s2r, r2s := d.s2r, d.r2s
	d.mu.Unlock()
	if len(s2r) > 0 {
		writeWAV(filepath.Join(d.dir, "s2r_"+tag+".wav"), s2r)
	}
	if len(r2s) > 0 {
		writeWAV(filepath.Join(d.dir, "r2s_"+tag+".wav"), r2s)
	}
}

// writeWAV writes 16-bit mono PCM at the media sample rate.
func writeWAV(path string, pcm []int16) {
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	data := make([]byte, 2*len(pcm))
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(data[2*i:], uint16(v))
	}
	var h [44]byte
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(data)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], 1) // mono
	binary.LittleEndian.PutUint32(h[24:], SampleRate)
	binary.LittleEndian.PutUint32(h[28:], SampleRate*2)
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(data)))
	f.Write(h[:])
	f.Write(data)
}

// dumpTag renders a file-name-safe call start timestamp.
func dumpTag(t time.Time) string { return t.Format("20060102-150405") }
