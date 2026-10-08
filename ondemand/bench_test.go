package ondemand_test

// The six C++ simdjson On-Demand benchmark tasks (benchmark/*/simdjson_ondemand.h
// and simdjson_dom.h), each written with On-Demand and with Parse plus the
// DOM. TestTasksAgree checks both compute the same answer.

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"sync"
	"testing"

	"simdjson-go"
	"simdjson-go/ondemand"
)

func twitter(tb testing.TB) []byte {
	data, err := os.ReadFile("../testdata/jsonexamples/twitter.json")
	if err != nil {
		tb.Fatal(err) // run `make testdata` first
	}
	return data
}

// --- inputs generated like C++'s (same shape, not the same digits) ---

var kostyaJSON = sync.OnceValue(func() []byte { return buildKostya(524288) })
var largeRandomJSON = sync.OnceValue(func() []byte { return buildLargeRandom(1000000) })

func buildKostya(n int) []byte {
	r := rand.New(rand.NewPCG(1, 2))
	var b bytes.Buffer
	b.WriteString("{\n    \"coordinates\": [\n")
	for i := range n {
		fmt.Fprintf(&b, "        {\n            \"x\": %v,\n            \"y\": %v,\n            \"z\": %v,\n", r.Float64(), r.Float64(), r.Float64())
		name := make([]byte, 6)
		for j := range name {
			name[j] = byte('a' + r.IntN(25))
		}
		fmt.Fprintf(&b, "            \"name\": \"%s %d\",\n", name, r.IntN(10000))
		b.WriteString("            \"opts\": {\n                \"1\": [\n                    1,\n                    true\n                ]\n            }\n        }")
		if i < n-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("    ],\n    \"info\": \"some info\"\n}\n")
	return b.Bytes()
}

func buildLargeRandom(n int) []byte {
	r := rand.New(rand.NewPCG(3, 4))
	var b bytes.Buffer
	b.WriteString("[\n")
	for i := range n {
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, "{ \"x\":%v,  \"y\":%v, \"z\":%v}", r.Float64(), r.Float64(), r.Float64())
	}
	b.WriteString("\n]\n")
	return b.Bytes()
}

// --- results ---

type tweet struct {
	CreatedAt, Text, ScreenName string
	ID, InReplyTo, UserID       uint64
	RetweetCount, FavoriteCount uint64
}

type point struct{ X, Y, Z float64 }

type topTweet struct {
	RetweetCount     int64
	Text, ScreenName string
}

// Results hold []byte views (valid until the next parse) during a run;
// keep converts them for comparison.
type tweetView struct {
	createdAt, text, screenName []byte
	id, inReplyTo, userID       uint64
	retweetCount, favoriteCount uint64
}

func keepTweets(v []tweetView) []tweet {
	out := make([]tweet, len(v))
	for i, t := range v {
		out[i] = tweet{string(t.createdAt), string(t.text), string(t.screenName), t.id, t.inReplyTo, t.userID, t.retweetCount, t.favoriteCount}
	}
	return out
}

// --- On-Demand implementations ---

func odPartialTweets(p *ondemand.Parser, data []byte, out []tweetView) ([]tweetView, error) {
	doc, err := p.Iterate(data)
	if err != nil {
		return nil, err
	}
	statuses, err := doc.FindNext("statuses")
	if err != nil {
		return nil, err
	}
	arr, err := statuses.Array()
	if err != nil {
		return nil, err
	}
	for tw, err := range arr.All() {
		if err != nil {
			return nil, err
		}
		var t tweetView
		t.createdAt = must(must(tw.FindNext("created_at")).StringBytes())
		t.id = must(must(tw.FindNext("id")).Uint64())
		t.text = must(must(tw.FindNext("text")).StringBytes())
		reply := must(tw.FindNext("in_reply_to_status_id"))
		if !must(reply.IsNull()) {
			t.inReplyTo = must(reply.Uint64())
		}
		user := must(tw.FindNext("user"))
		t.userID = must(must(user.FindNext("id")).Uint64())
		t.screenName = must(must(user.FindNext("screen_name")).StringBytes())
		t.retweetCount = must(must(tw.FindNext("retweet_count")).Uint64())
		t.favoriteCount = must(must(tw.FindNext("favorite_count")).Uint64())
		out = append(out, t)
	}
	return out, nil
}

func odDistinctUserID(p *ondemand.Parser, data []byte, out []uint64) ([]uint64, error) {
	doc, err := p.Iterate(data)
	if err != nil {
		return nil, err
	}
	arr := must(must(doc.FindNext("statuses")).Array())
	for tw, err := range arr.All() {
		if err != nil {
			return nil, err
		}
		out = append(out, must(must(must(tw.FindNext("user")).FindNext("id")).Uint64()))
		if rt, err := tw.FindNext("retweeted_status"); err == nil {
			out = append(out, must(must(must(rt.FindNext("user")).FindNext("id")).Uint64()))
		}
	}
	return out, nil
}

const findID = 505874901689851904

func odFindTweet(p *ondemand.Parser, data []byte) ([]byte, error) {
	doc, err := p.Iterate(data)
	if err != nil {
		return nil, err
	}
	arr := must(must(doc.FindNext("statuses")).Array())
	for tw, err := range arr.All() {
		if err != nil {
			return nil, err
		}
		if must(must(tw.FindNext("id")).Uint64()) == findID {
			return must(tw.FindNext("text")).StringBytes()
		}
	}
	return nil, nil
}

const maxRetweets = 60

func odTopTweet(p *ondemand.Parser, data []byte) (int64, []byte, []byte, error) {
	doc, err := p.Iterate(data)
	if err != nil {
		return 0, nil, nil, err
	}
	best := int64(-1)
	var text, screenName ondemand.Value // scalars, read after the loop
	arr := must(must(doc.Get("statuses")).Array())
	for tw, err := range arr.All() {
		if err != nil {
			return 0, nil, nil, err
		}
		t := must(tw.Get("text"))
		sn := must(must(tw.Get("user")).Get("screen_name"))
		n := must(must(tw.Get("retweet_count")).Int64())
		if n <= maxRetweets && n >= best {
			best, text, screenName = n, t, sn
		}
	}
	if best < 0 {
		return best, nil, nil, nil
	}
	return best, must(text.StringBytes()), must(screenName.StringBytes()), nil
}

func odKostya(p *ondemand.Parser, data []byte, out []point) ([]point, error) {
	doc, err := p.Iterate(data)
	if err != nil {
		return nil, err
	}
	arr := must(must(doc.FindNext("coordinates")).Array())
	for pt, err := range arr.All() {
		if err != nil {
			return nil, err
		}
		out = append(out, point{must(must(pt.FindNext("x")).Float64()), must(must(pt.FindNext("y")).Float64()), must(must(pt.FindNext("z")).Float64())})
	}
	return out, nil
}

func odLargeRandom(p *ondemand.Parser, data []byte, out []point) ([]point, error) {
	doc, err := p.Iterate(data)
	if err != nil {
		return nil, err
	}
	arr := must(doc.Array())
	for pt, err := range arr.All() {
		if err != nil {
			return nil, err
		}
		out = append(out, point{must(must(pt.FindNext("x")).Float64()), must(must(pt.FindNext("y")).Float64()), must(must(pt.FindNext("z")).Float64())})
	}
	return out, nil
}

// --- DOM implementations ---

func get(e simdjson.Element, key string) simdjson.Element {
	return must(must(e.Object()).Get(key))
}

func domPartialTweets(p *simdjson.Parser, data []byte, out []tweetView) ([]tweetView, error) {
	doc, err := p.Parse(data)
	if err != nil {
		return nil, err
	}
	for _, tw := range must(get(doc.Root(), "statuses").Array()).All() {
		o := must(tw.Object())
		var t tweetView
		t.createdAt = must(must(o.Get("created_at")).StringBytes())
		t.id = must(must(o.Get("id")).Uint64())
		t.text = must(must(o.Get("text")).StringBytes())
		if reply := must(o.Get("in_reply_to_status_id")); !reply.IsNull() {
			t.inReplyTo = must(reply.Uint64())
		}
		user := must(must(o.Get("user")).Object())
		t.userID = must(must(user.Get("id")).Uint64())
		t.screenName = must(must(user.Get("screen_name")).StringBytes())
		t.retweetCount = must(must(o.Get("retweet_count")).Uint64())
		t.favoriteCount = must(must(o.Get("favorite_count")).Uint64())
		out = append(out, t)
	}
	return out, nil
}

func domDistinctUserID(p *simdjson.Parser, data []byte, out []uint64) ([]uint64, error) {
	doc, err := p.Parse(data)
	if err != nil {
		return nil, err
	}
	for _, tw := range must(get(doc.Root(), "statuses").Array()).All() {
		o := must(tw.Object())
		out = append(out, must(get(must(o.Get("user")), "id").Uint64()))
		if rt, err := o.Get("retweeted_status"); err == nil {
			out = append(out, must(get(get(rt, "user"), "id").Uint64()))
		}
	}
	return out, nil
}

func domFindTweet(p *simdjson.Parser, data []byte) ([]byte, error) {
	doc, err := p.Parse(data)
	if err != nil {
		return nil, err
	}
	for _, tw := range must(get(doc.Root(), "statuses").Array()).All() {
		if must(get(tw, "id").Uint64()) == findID {
			return get(tw, "text").StringBytes()
		}
	}
	return nil, nil
}

func domTopTweet(p *simdjson.Parser, data []byte) (int64, []byte, []byte, error) {
	doc, err := p.Parse(data)
	if err != nil {
		return 0, nil, nil, err
	}
	best := int64(-1)
	var text, screenName simdjson.Element
	for _, tw := range must(get(doc.Root(), "statuses").Array()).All() {
		n := must(get(tw, "retweet_count").Int64())
		if n <= maxRetweets && n >= best {
			best, text, screenName = n, get(tw, "text"), get(get(tw, "user"), "screen_name")
		}
	}
	if best < 0 {
		return best, nil, nil, nil
	}
	return best, must(text.StringBytes()), must(screenName.StringBytes()), nil
}

func domPoints(arr simdjson.Array, out []point) []point {
	for _, pt := range arr.All() {
		o := must(pt.Object())
		out = append(out, point{must(must(o.Get("x")).Float64()), must(must(o.Get("y")).Float64()), must(must(o.Get("z")).Float64())})
	}
	return out
}

func domKostya(p *simdjson.Parser, data []byte, out []point) ([]point, error) {
	doc, err := p.Parse(data)
	if err != nil {
		return nil, err
	}
	return domPoints(must(get(doc.Root(), "coordinates").Array()), out), nil
}

func domLargeRandom(p *simdjson.Parser, data []byte, out []point) ([]point, error) {
	doc, err := p.Parse(data)
	if err != nil {
		return nil, err
	}
	return domPoints(must(doc.Root().Array()), out), nil
}

// --- agreement ---

func TestTasksAgree(t *testing.T) {
	data := twitter(t)
	var od ondemand.Parser
	var dom simdjson.Parser
	check := func(name string, a, b any) {
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s: On-Demand %v\nDOM %v", name, a, b)
		}
	}
	a, err := odPartialTweets(&od, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := domPartialTweets(&dom, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 100 {
		t.Errorf("partial_tweets: %d tweets", len(a))
	}
	check("partial_tweets", keepTweets(a), keepTweets(b))
	check("distinct_user_id", must(odDistinctUserID(&od, data, nil)), must(domDistinctUserID(&dom, data, nil)))
	check("find_tweet", string(must(odFindTweet(&od, data))), string(must(domFindTweet(&dom, data))))
	n1, t1, s1, _ := odTopTweet(&od, data)
	n2, t2, s2, _ := domTopTweet(&dom, data)
	check("top_tweet", topTweet{n1, string(t1), string(s1)}, topTweet{n2, string(t2), string(s2)})
	if n1 < 0 {
		t.Error("top_tweet: none found")
	}
	// the benchmarked inputs; -short uses small ones
	k, lr := kostyaJSON(), largeRandomJSON()
	if testing.Short() {
		k, lr = buildKostya(1000), buildLargeRandom(1000)
	}
	check("kostya", must(odKostya(&od, k, nil)), must(domKostya(&dom, k, nil)))
	check("large_random", must(odLargeRandom(&od, lr, nil)), must(domLargeRandom(&dom, lr, nil)))
}

// --- benchmarks ---

func BenchmarkTasks(b *testing.B) {
	for _, task := range []struct {
		name string
		data func(testing.TB) []byte
		od   func(*ondemand.Parser, []byte) error
		dom  func(*simdjson.Parser, []byte) error
	}{
		{"partial_tweets", twitter, sliceTask(odPartialTweets), sliceTask(domPartialTweets)},
		{"distinct_user_id", twitter, sliceTask(odDistinctUserID), sliceTask(domDistinctUserID)},
		{"find_tweet", twitter,
			func(p *ondemand.Parser, d []byte) error { _, err := odFindTweet(p, d); return err },
			func(p *simdjson.Parser, d []byte) error { _, err := domFindTweet(p, d); return err }},
		{"top_tweet", twitter,
			func(p *ondemand.Parser, d []byte) error { _, _, _, err := odTopTweet(p, d); return err },
			func(p *simdjson.Parser, d []byte) error { _, _, _, err := domTopTweet(p, d); return err }},
		{"kostya", func(testing.TB) []byte { return kostyaJSON() }, sliceTask(odKostya), sliceTask(domKostya)},
		{"large_random", func(testing.TB) []byte { return largeRandomJSON() }, sliceTask(odLargeRandom), sliceTask(domLargeRandom)},
	} {
		b.Run(task.name+"/ondemand", func(b *testing.B) {
			data := task.data(b)
			var p ondemand.Parser
			run(b, data, func(d []byte) error { return task.od(&p, d) })
		})
		b.Run(task.name+"/dom", func(b *testing.B) {
			data := task.data(b)
			var p simdjson.Parser
			run(b, data, func(d []byte) error { return task.dom(&p, d) })
		})
	}
}

// sliceTask adapts a task that appends to a result slice, reusing the slice.
func sliceTask[P any, T any](f func(P, []byte, []T) ([]T, error)) func(P, []byte) error {
	var out []T
	return func(p P, d []byte) (err error) {
		out, err = f(p, d, out[:0])
		return err
	}
}

func run(b *testing.B, data []byte, f func([]byte) error) {
	if err := f(data); err != nil { // warm up the buffers
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if err := f(data); err != nil {
			b.Fatal(err)
		}
	}
}
