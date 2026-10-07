package simdjson

import (
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
	var idx []uint32
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		var err error
		if idx, err = stage1.Index(data, idx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMinify(b *testing.B) {
	data := readTestdata(b, "jsonexamples", "twitter.json")
	var dst []byte
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		var err error
		if dst, err = Minify(dst[:0], data); err != nil {
			b.Fatal(err)
		}
	}
}
