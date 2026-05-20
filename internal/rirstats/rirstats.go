package rirstats

import (
	"bufio"
	"encoding/binary"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type v4Range struct {
	start uint32
	end   uint32
	cc    string
}

type Index struct {
	v4 []v4Range
}

func LoadDir(dir string) (*Index, error) {
	files, err := filepath.Glob(filepath.Join(dir, "delegated-*-extended-latest"))
	if err != nil {
		return nil, err
	}
	idx := &Index{}
	for _, path := range files {
		if err := idx.loadFile(path); err != nil {
			return nil, err
		}
	}
	sort.Slice(idx.v4, func(i, j int) bool {
		return idx.v4[i].start < idx.v4[j].start
	})
	return idx, nil
}

func (idx *Index) Loaded() bool {
	return idx != nil && len(idx.v4) > 0
}

func (idx *Index) Country(addr netip.Addr) string {
	if idx == nil || !addr.Is4() {
		return ""
	}
	ip := addr.As4()
	n := binary.BigEndian.Uint32(ip[:])
	i := sort.Search(len(idx.v4), func(i int) bool {
		return idx.v4[i].start > n
	})
	if i == 0 {
		return ""
	}
	r := idx.v4[i-1]
	if n >= r.start && n <= r.end {
		return r.cc
	}
	return ""
}

func (idx *Index) loadFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Split(line, "|")
		if len(p) < 7 || p[2] != "ipv4" {
			continue
		}
		cc := strings.ToUpper(strings.TrimSpace(p[1]))
		if len(cc) != 2 || cc == "ZZ" {
			continue
		}
		start, err := netip.ParseAddr(p[3])
		if err != nil || !start.Is4() {
			continue
		}
		count, err := strconv.ParseUint(p[4], 10, 32)
		if err != nil || count == 0 {
			continue
		}
		b := start.As4()
		startN := binary.BigEndian.Uint32(b[:])
		endN := startN + uint32(count) - 1
		idx.v4 = append(idx.v4, v4Range{start: startN, end: endN, cc: cc})
	}
	return sc.Err()
}
