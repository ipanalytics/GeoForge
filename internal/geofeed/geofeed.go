package geofeed

import (
	"encoding/binary"
	"encoding/csv"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const defaultMaxIPv4Bits = 24

type Entry struct {
	Prefix  netip.Prefix
	Country string
	State   string
	City    string
	Postal  string
	start   uint32
	end     uint32
}

type Index struct {
	v4 []Entry
}

func LoadDir(dir string) (*Index, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.csv"))
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
		if idx.v4[i].start != idx.v4[j].start {
			return idx.v4[i].start < idx.v4[j].start
		}
		return idx.v4[i].Prefix.Bits() > idx.v4[j].Prefix.Bits()
	})
	return idx, nil
}

func (idx *Index) Loaded() bool {
	return idx != nil && len(idx.v4) > 0
}

func (idx *Index) Lookup(prefix netip.Prefix) *Entry {
	if idx == nil || !prefix.Addr().Is4() {
		return nil
	}
	ip := prefix.Addr().As4()
	n := binary.BigEndian.Uint32(ip[:])
	i := sort.Search(len(idx.v4), func(i int) bool {
		return idx.v4[i].start > n
	})
	var best *Entry
	for j := i - 1; j >= 0; j-- {
		e := &idx.v4[j]
		if n >= e.start && n <= e.end && e.Prefix.Contains(prefix.Addr()) {
			if best == nil || e.Prefix.Bits() > best.Prefix.Bits() {
				best = e
			}
		}
	}
	return best
}

func (idx *Index) loadFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	for {
		rec, err := r.Read()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if len(rec) == 0 || strings.HasPrefix(strings.TrimSpace(rec[0]), "#") {
			continue
		}
		if len(rec) < 4 {
			continue
		}
		pfx, err := netip.ParsePrefix(strings.TrimSpace(rec[0]))
		if err != nil || !pfx.Addr().Is4() {
			continue
		}
		if pfx.Bits() > maxIPv4Bits() {
			continue
		}
		postal := ""
		if len(rec) >= 5 {
			postal = strings.TrimSpace(rec[4])
		}
		start, end := bounds4(pfx)
		idx.v4 = append(idx.v4, Entry{
			Prefix:  pfx,
			Country: strings.ToUpper(strings.TrimSpace(rec[1])),
			State:   strings.TrimSpace(rec[2]),
			City:    strings.TrimSpace(rec[3]),
			Postal:  postal,
			start:   start,
			end:     end,
		})
	}
}

func maxIPv4Bits() int {
	raw := strings.TrimSpace(os.Getenv("GEOFEED_MAX_IPV4_BITS"))
	if raw == "" {
		return defaultMaxIPv4Bits
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || n > 32 {
		return defaultMaxIPv4Bits
	}
	return n
}

func bounds4(p netip.Prefix) (uint32, uint32) {
	ip := p.Addr().As4()
	start := binary.BigEndian.Uint32(ip[:])
	hostBits := 32 - p.Bits()
	if hostBits <= 0 {
		return start, start
	}
	return start, start | ((uint32(1) << hostBits) - 1)
}
