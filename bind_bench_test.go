package simdjson

import (
	jsonv2 "encoding/json/v2"
	"testing"
)

// Typed subsets of the example files, as a service would declare them.
type benchUser struct {
	ID         uint64 `json:"id"`
	Name       string `json:"name"`
	ScreenName string `json:"screen_name"`
	Followers  int    `json:"followers_count"`
	Verified   bool   `json:"verified"`
}
type benchStatus struct {
	ID        uint64    `json:"id"`
	Text      string    `json:"text"`
	CreatedAt string    `json:"created_at"`
	User      benchUser `json:"user"`
	Retweets  int       `json:"retweet_count"`
	Favorites int       `json:"favorite_count"`
	Lang      string    `json:"lang"`
}
type benchTwitter struct {
	Statuses []benchStatus `json:"statuses"`
}

type benchCitm struct {
	AreaNames                map[string]string `json:"areaNames"`
	AudienceSubCategoryNames map[string]string `json:"audienceSubCategoryNames"`
	Events                   map[string]struct {
		ID          int64   `json:"id"`
		Name        string  `json:"name"`
		Description *string `json:"description"`
		Logo        *string `json:"logo"`
		SubTopicIDs []int64 `json:"subTopicIds"`
		TopicIDs    []int64 `json:"topicIds"`
	} `json:"events"`
	Performances []struct {
		EventID int64 `json:"eventId"`
		ID      int64 `json:"id"`
		Prices  []struct {
			Amount                int64 `json:"amount"`
			AudienceSubCategoryID int64 `json:"audienceSubCategoryId"`
			SeatCategoryID        int64 `json:"seatCategoryId"`
		} `json:"prices"`
		SeatCategories []struct {
			Areas []struct {
				AreaID   int64   `json:"areaId"`
				BlockIDs []int64 `json:"blockIds"`
			} `json:"areas"`
			SeatCategoryID int64 `json:"seatCategoryId"`
		} `json:"seatCategories"`
		Start     int64  `json:"start"`
		VenueCode string `json:"venueCode"`
	} `json:"performances"`
	SeatCategoryNames map[string]string  `json:"seatCategoryNames"`
	SubTopicNames     map[string]string  `json:"subTopicNames"`
	TopicNames        map[string]string  `json:"topicNames"`
	TopicSubTopics    map[string][]int64 `json:"topicSubTopics"`
	VenueNames        map[string]string  `json:"venueNames"`
}

type benchCanada struct {
	Type     string `json:"type"`
	Features []struct {
		Type       string `json:"type"`
		Properties struct {
			Name string `json:"name"`
		} `json:"properties"`
		Geometry struct {
			Type        string         `json:"type"`
			Coordinates [][][2]float64 `json:"coordinates"`
		} `json:"geometry"`
	} `json:"features"`
}

func benchBind[T any](b *testing.B, file string, unmarshal func([]byte, any) error) {
	data := readTestdata(b, "jsonexamples", file)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		var v T
		if err := unmarshal(data, &v); err != nil {
			b.Fatal(err)
		}
	}
}

func ours(data []byte, v any) error { return Unmarshal(data, v) }
func v2(data []byte, v any) error   { return jsonv2.Unmarshal(data, v) }

func BenchmarkUnmarshal(b *testing.B) {
	b.Run("twitter", func(b *testing.B) { benchBind[benchTwitter](b, "twitter.json", ours) })
	b.Run("citm", func(b *testing.B) { benchBind[benchCitm](b, "citm_catalog.json", ours) })
	b.Run("canada", func(b *testing.B) { benchBind[benchCanada](b, "canada.json", ours) })
}

func BenchmarkV2Unmarshal(b *testing.B) {
	b.Run("twitter", func(b *testing.B) { benchBind[benchTwitter](b, "twitter.json", v2) })
	b.Run("citm", func(b *testing.B) { benchBind[benchCitm](b, "citm_catalog.json", v2) })
	b.Run("canada", func(b *testing.B) { benchBind[benchCanada](b, "canada.json", v2) })
}

// TestBenchTypesMatchV2 keeps the benchmarks honest: both decode the same,
// and our Marshal output equals v2's.
func TestBenchTypesMatchV2(t *testing.T) {
	check := func(file string, a, b any) {
		data := readTestdata(t, "jsonexamples", file)
		if err := Unmarshal(data, a); err != nil {
			t.Fatal(file, err)
		}
		if err := jsonv2.Unmarshal(data, b); err != nil {
			t.Fatal(file, err)
		}
		x, err := jsonv2.Marshal(a, jsonv2.Deterministic(true))
		if err != nil {
			t.Fatal(file, err)
		}
		y, err := jsonv2.Marshal(b, jsonv2.Deterministic(true))
		if err != nil {
			t.Fatal(file, err)
		}
		if string(x) != string(y) {
			t.Errorf("%s: decoded values differ", file)
		}
		z, err := Marshal(b, Deterministic(true))
		if err != nil {
			t.Fatal(file, err)
		}
		if string(z) != string(y) {
			t.Errorf("%s: Marshal output differs from v2", file)
		}
		if len(y) < 1000 {
			t.Errorf("%s: only %d bytes re-encoded from %d", file, len(y), len(data))
		}
	}
	check("twitter.json", new(benchTwitter), new(benchTwitter))
	check("citm_catalog.json", new(benchCitm), new(benchCitm))
	check("canada.json", new(benchCanada), new(benchCanada))
}

func benchMarshal[T any](b *testing.B, file string, marshal func(any) ([]byte, error)) {
	var v T
	if err := jsonv2.Unmarshal(readTestdata(b, "jsonexamples", file), &v); err != nil {
		b.Fatal(err)
	}
	out, _ := marshal(&v)
	b.SetBytes(int64(len(out)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := marshal(&v); err != nil {
			b.Fatal(err)
		}
	}
}

func oursM(v any) ([]byte, error) { return Marshal(v) }
func v2M(v any) ([]byte, error)   { return jsonv2.Marshal(v) }

func BenchmarkMarshal(b *testing.B) {
	b.Run("twitter", func(b *testing.B) { benchMarshal[benchTwitter](b, "twitter.json", oursM) })
	b.Run("citm", func(b *testing.B) { benchMarshal[benchCitm](b, "citm_catalog.json", oursM) })
	b.Run("canada", func(b *testing.B) { benchMarshal[benchCanada](b, "canada.json", oursM) })
}

func BenchmarkV2Marshal(b *testing.B) {
	b.Run("twitter", func(b *testing.B) { benchMarshal[benchTwitter](b, "twitter.json", v2M) })
	b.Run("citm", func(b *testing.B) { benchMarshal[benchCitm](b, "citm_catalog.json", v2M) })
	b.Run("canada", func(b *testing.B) { benchMarshal[benchCanada](b, "canada.json", v2M) })
}

// BenchmarkParseMode shows what binding mode adds to parsing.
func BenchmarkParseMode(b *testing.B) {
	data := readTestdata(b, "jsonexamples", "twitter.json")
	for _, binding := range []bool{false, true} {
		b.Run(map[bool]string{false: "plain", true: "binding"}[binding], func(b *testing.B) {
			p := Parser{MaxDepth: 10001, BigIntAsString: true, binding: binding}
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := p.Parse(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
