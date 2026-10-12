package backup

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"
	"io"
	"os"
)

// spool holds the members of a tar on disk while they are written or read, so
// memory stays one record (or one blob) whatever the size of the backup. The
// plaintext never reaches the disk: the file is encrypted with AES-CTR under a
// key that lives only in this process, and each member starts at a multiple
// of the block size so it is read from its own offset. The file is removed on
// close; a process that dies leaves only ciphertext whose key died with it.
type spool struct {
	f     *os.File
	block cipher.Block
	w     *bufio.Writer
	end   int64

	// the member being written
	open  bool
	start int64
	ctr   cipher.Stream
	h     hash.Hash
	buf   []byte
}

// span is one member in the spool.
type span struct {
	off, size int64
	sum       [32]byte
}

func newSpool(dir string) (*spool, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, "sluis-backup-spool-*")
	if err != nil {
		return nil, err
	}
	return &spool{f: f, block: block, w: bufio.NewWriterSize(f, 256<<10)}, nil
}

// stream is the key stream from byte off, a multiple of the block size.
func (s *spool) stream(off int64) cipher.Stream {
	iv := make([]byte, aes.BlockSize)
	binary.BigEndian.PutUint64(iv[8:], uint64(off/aes.BlockSize))
	return cipher.NewCTR(s.block, iv)
}

func (s *spool) begin() {
	s.open, s.start, s.ctr, s.h = true, s.end, s.stream(s.end), sha256.New()
}

func (s *spool) Write(p []byte) (int, error) {
	if !s.open {
		return 0, errors.New("backup: spool: no member is open")
	}
	s.h.Write(p)
	for rest := p; len(rest) > 0; {
		n := min(len(rest), 64<<10)
		if cap(s.buf) < n {
			s.buf = make([]byte, 64<<10)
		}
		b := s.buf[:n]
		s.ctr.XORKeyStream(b, rest[:n])
		if _, err := s.w.Write(b); err != nil {
			return 0, err
		}
		rest = rest[n:]
	}
	s.end += int64(len(p))
	return len(p), nil
}

// finish closes the member and pads the file to the next block.
func (s *spool) finish() (span, error) {
	sp := span{off: s.start, size: s.end - s.start}
	copy(sp.sum[:], s.h.Sum(nil))
	s.open, s.ctr, s.h = false, nil, nil
	if pad := (aes.BlockSize - s.end%aes.BlockSize) % aes.BlockSize; pad > 0 {
		if _, err := s.w.Write(make([]byte, pad)); err != nil {
			return span{}, err
		}
		s.end += pad
	}
	return sp, nil
}

// reader reads a member back; the caller checks what it reads against the
// member's hash.
func (s *spool) reader(sp span) (io.Reader, error) {
	if err := s.w.Flush(); err != nil {
		return nil, err
	}
	return cipher.StreamReader{S: s.stream(sp.off), R: io.NewSectionReader(s.f, sp.off, sp.size)}, nil
}

func (s *spool) close() {
	if s == nil || s.f == nil {
		return
	}
	name := s.f.Name()
	_ = s.f.Close()
	_ = os.Remove(name)
	s.f = nil
}
