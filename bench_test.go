package simdjson

import (
	"bytes"
	"encoding/json"
	"testing"

	"simdjson-go/internal/stage1"
)

var benchFiles = []string{"twitter.json", "citm_catalog.json", "canada.json", "github_events.json", "gsoc-2018.json", "update-center.json"}

func BenchmarkParse(b *testing.B) {
	for _, name := range benchFiles {
		b.Run(name, func(b *testing.B) {
			data := readTestdata(b, "jsonexamples", name)
			var p Parser
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			if _, err := p.Parse(data); err != nil {
				b.Fatal(err)
			}
			for b.Loop() {
				if _, err := p.Parse(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkStdlib(b *testing.B) {
	for _, name := range benchFiles {
		b.Run(name, func(b *testing.B) {
			data := readTestdata(b, "jsonexamples", name)
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for b.Loop() {
				var v any
				if err := json.Unmarshal(data, &v); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkIndex(b *testing.B) {
	data := readTestdata(b, "jsonexamples", "twitter.json")
	idx, err := stage1.Index(data, nil) // warm up: grow idx once
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if idx, err = stage1.Index(data, idx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMinify(b *testing.B) {
	data := readTestdata(b, "jsonexamples", "twitter.json")
	dst, err := Minify(nil, data) // warm up: grow dst once
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if dst, err = Minify(dst[:0], data); err != nil {
			b.Fatal(err)
		}
	}
}

type brand struct {
	rating  float64
	reviews uint64
}

// amazonDOM is C++'s amazon_cellphones benchmark (benchmark/amazon_cellphones):
// per brand, the sum of rating*reviews and of reviews, skipping the header line.
func amazonDOM(p *Parser, data []byte, out map[string]*brand) error {
	first := true
	for doc, err := range p.ParseMany(data, Whitespace) {
		if err != nil {
			return err
		}
		if first {
			first = false
			continue
		}
		arr, err := doc.Root().Array()
		if err != nil {
			return err
		}
		name, _ := arr.At(1)
		rating, _ := arr.At(5)
		reviews, _ := arr.At(7)
		s, _ := name.StringBytes()
		x, _ := rating.Float64()
		n, _ := reviews.Uint64()
		b := out[string(s)]
		if b == nil {
			b = &brand{}
			out[string(s)] = b
		}
		b.rating += x * float64(n)
		b.reviews += n
	}
	return nil
}

func BenchmarkParseMany(b *testing.B) {
	small := readTestdata(b, "jsonexamples", "amazon_cellphones.ndjson")
	for _, in := range []struct {
		name string
		data []byte
	}{{"amazon_cellphones", small}, {"large_amazon_cellphones", bytes.Repeat(small, 40)}} {
		for _, bs := range []struct {
			name string
			size int
		}{{"default", 0}, {"single", len(in.data)}} {
			b.Run(in.name+"/"+bs.name, func(b *testing.B) {
				p := Parser{BatchSize: bs.size}
				brands := map[string]*brand{}
				b.SetBytes(int64(len(in.data)))
				b.ReportAllocs()
				for b.Loop() {
					if err := amazonDOM(&p, in.data, brands); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
