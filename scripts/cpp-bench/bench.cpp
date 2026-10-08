// bench runs C++ simdjson v5.0.2 on the inputs of simdjson-go's benchmarks
// and prints the results in Go's benchmark format, under the names of the Go
// benchmarks, for benchstat to compare (run.sh). The task bodies are copied
// from v5.0.2's benchmark/<task>/simdjson_{dom,ondemand}.h, which the Go
// benchmarks port.
//
// usage: bench CORPUS_DIR GEN_DIR FILE...
//
// CORPUS_DIR holds twitter.json and amazon_cellphones.ndjson, GEN_DIR the
// kostya.json and large_random.json that TestWriteBenchInputs writes, and each
// FILE is an input of BenchmarkParse.
#include "simdjson.h"

#include <algorithm>
#include <chrono>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <exception>
#include <map>
#include <stdexcept>
#include <string>
#include <thread>
#include <utility>
#include <vector>

using namespace simdjson;

// --- timing ---

static unsigned procs = std::thread::hardware_concurrency();

// bench times f as Go's b.Loop does with -benchtime 1s: one untimed run, then
// runs of n iterations, n growing, until one takes at least a second. It
// prints one line in Go's format; any error exits.
template <typename F>
static void bench(const std::string &name, size_t bytes, F f) {
  try {
    f();
    for (uint64_t n = 1;;) {
      auto t0 = std::chrono::steady_clock::now();
      for (uint64_t i = 0; i < n; i++) { f(); }
      double ns = std::chrono::duration<double, std::nano>(std::chrono::steady_clock::now() - t0).count();
      if (ns >= 1e9 || n >= 1000000000) {
        std::printf("Benchmark%s-%u\t%llu\t%.1f ns/op\t%.2f MB/s\n", name.c_str(), procs,
                    (unsigned long long)n, ns / n, double(bytes) * n / ns * 1e3);
        std::fflush(stdout);
        return;
      }
      // As Go's predictN: aim 20% past a second, grow at most 100x (Go grows
      // by at least one iteration; this by at least 2x).
      uint64_t next = uint64_t(1.2e9 * n / (ns > 1 ? ns : 1));
      n = std::max(std::min(next, 100 * n), 2 * n);
    }
  } catch (const std::exception &e) {
    std::fprintf(stderr, "Benchmark%s: %s\n", name.c_str(), e.what());
    std::exit(1);
  }
}

static padded_string load(const std::string &path) {
  padded_string s;
  if (auto err = padded_string::load(path).get(s)) {
    std::fprintf(stderr, "%s: %s\n", path.c_str(), error_message(err));
    std::exit(1);
  }
  return s;
}

// --- results (benchmark/<task>/<task>.h) ---

struct twitter_user {
  uint64_t id{};
  std::string_view screen_name{};
};

struct tweet {
  std::string_view created_at{};
  uint64_t id{};
  std::string_view result{};
  uint64_t in_reply_to_status_id{};
  twitter_user user{};
  uint64_t retweet_count{};
  uint64_t favorite_count{};
};

struct top_tweet_result {
  int64_t retweet_count{};
  std::string_view screen_name{};
  std::string_view text{};
};

struct point {
  double x, y, z;
};

struct brand {
  double cumulative_rating;
  uint64_t reviews_count;
};

// --- partial_tweets ---

struct partial_tweets_ondemand {
  ondemand::parser parser{};
  simdjson_inline uint64_t nullable_int(ondemand::value value) {
    if (value.is_null()) { return 0; }
    return value;
  }
  simdjson_inline twitter_user read_user(ondemand::object user) {
    return { user.find_field("id"), user.find_field("screen_name") };
  }
  bool run(padded_string &json, std::vector<tweet> &result) {
    auto doc = parser.iterate(json);
    for (ondemand::object tweet : doc.find_field("statuses")) {
      result.emplace_back(::tweet{
        tweet.find_field("created_at"),
        tweet.find_field("id"),
        tweet.find_field("text"),
        nullable_int(tweet.find_field("in_reply_to_status_id")),
        read_user(tweet.find_field("user")),
        tweet.find_field("retweet_count"),
        tweet.find_field("favorite_count")
      });
    }
    return true;
  }
};

struct partial_tweets_dom {
  dom::parser parser{};
  simdjson_inline uint64_t nullable_int(dom::element element) {
    if (element.is_null()) { return 0; }
    return element;
  }
  bool run(padded_string &json, std::vector<tweet> &result) {
    for (dom::element tweet : parser.parse(json)["statuses"]) {
      auto user = tweet["user"];
      result.emplace_back(::tweet{
        tweet["created_at"],
        tweet["id"],
        tweet["text"],
        nullable_int(tweet["in_reply_to_status_id"]),
        { user["id"], user["screen_name"] },
        tweet["retweet_count"],
        tweet["favorite_count"]
      });
    }
    return true;
  }
};

// --- distinct_user_id ---

struct distinct_user_id_ondemand {
  ondemand::parser parser{};
  bool run(padded_string &json, std::vector<uint64_t> &result) {
    auto doc = parser.iterate(json);
    for (ondemand::object tweet : doc.find_field("statuses")) {
      result.push_back(tweet.find_field("user").find_field("id"));
      auto retweet = tweet.find_field("retweeted_status");
      if (!retweet.error()) {
        result.push_back(retweet.find_field("user").find_field("id"));
      }
    }
    return true;
  }
};

struct distinct_user_id_dom {
  dom::parser parser{};
  bool run(padded_string &json, std::vector<uint64_t> &result) {
    auto doc = parser.parse(json);
    for (dom::object tweet : doc["statuses"]) {
      result.push_back(tweet["user"]["id"]);
      auto retweet = tweet["retweeted_status"];
      if (retweet.error() != NO_SUCH_FIELD) {
        result.push_back(retweet["user"]["id"]);
      }
    }
    return true;
  }
};

// --- find_tweet ---

struct find_tweet_ondemand {
  ondemand::parser parser{};
  bool run(padded_string &json, uint64_t find_id, std::string_view &result) {
    auto doc = parser.iterate(json);
    for (auto tweet : doc.find_field("statuses")) {
      if (uint64_t(tweet.find_field("id")) == find_id) {
        result = tweet.find_field("text");
        return true;
      }
    }
    return false;
  }
};

struct find_tweet_dom {
  dom::parser parser{};
  bool run(padded_string &json, uint64_t find_id, std::string_view &result) {
    result = "";
    auto doc = parser.parse(json);
    for (auto tweet : doc["statuses"]) {
      if (uint64_t(tweet["id"]) == find_id) {
        result = tweet["text"];
        return true;
      }
    }
    return false;
  }
};

// --- top_tweet ---

struct top_tweet_ondemand {
  ondemand::parser parser{};
  bool run(padded_string &json, int64_t max_retweet_count, top_tweet_result &result) {
    result.retweet_count = -1;
    ondemand::value screen_name, text;
    auto doc = parser.iterate(json);
    for (auto tweet : doc["statuses"]) {
      auto tweet_text = tweet["text"];
      auto tweet_screen_name = tweet["user"]["screen_name"];
      int64_t retweet_count = tweet["retweet_count"];
      if (retweet_count <= max_retweet_count && retweet_count >= result.retweet_count) {
        result.retweet_count = retweet_count;
        text = std::move(tweet_text);
        screen_name = std::move(tweet_screen_name);
      }
    }
    result.screen_name = screen_name;
    result.text = text;
    return result.retweet_count != -1;
  }
};

struct top_tweet_dom {
  dom::parser parser{};
  bool run(padded_string &json, int64_t max_retweet_count, top_tweet_result &result) {
    result.retweet_count = -1;
    dom::element top_tweet{};
    auto doc = parser.parse(json);
    for (auto tweet : doc["statuses"]) {
      int64_t retweet_count = tweet["retweet_count"];
      if (retweet_count <= max_retweet_count && retweet_count >= result.retweet_count) {
        result.retweet_count = retweet_count;
        top_tweet = tweet;
      }
    }
    result.text = top_tweet["text"];
    result.screen_name = top_tweet["user"]["screen_name"];
    return result.retweet_count != -1;
  }
};

// --- kostya ---

struct kostya_ondemand {
  ondemand::parser parser{};
  bool run(padded_string &json, std::vector<point> &result) {
    auto doc = parser.iterate(json);
    for (ondemand::object point : doc.find_field("coordinates")) {
      result.emplace_back(::point{point.find_field("x"), point.find_field("y"), point.find_field("z")});
    }
    return true;
  }
};

struct kostya_dom {
  dom::parser parser{};
  bool run(padded_string &json, std::vector<point> &result) {
    for (auto point : parser.parse(json)["coordinates"]) {
      result.emplace_back(::point{point["x"], point["y"], point["z"]});
    }
    return true;
  }
};

// --- large_random ---

struct large_random_ondemand {
  ondemand::parser parser{};
  bool run(padded_string &json, std::vector<point> &result) {
    auto doc = parser.iterate(json);
    for (ondemand::object coord : doc) {
      result.emplace_back(::point{coord.find_field("x"), coord.find_field("y"), coord.find_field("z")});
    }
    return true;
  }
};

struct large_random_dom {
  dom::parser parser{};
  bool run(padded_string &json, std::vector<point> &result) {
    for (auto point : parser.parse(json)) {
      result.emplace_back(::point{point["x"], point["y"], point["z"]});
    }
    return true;
  }
};

// --- amazon_cellphones, with the batch size as a parameter ---

struct amazon_cellphones_dom {
  dom::parser parser{};
  bool run(padded_string &json, size_t batch_size, std::map<std::string, brand> &result) {
    parser.threaded = true;
    auto stream = parser.parse_many(json, batch_size);
    auto i = stream.begin();
    ++i;  // Skip first line
    for (; i != stream.end(); ++i) {
      auto doc = *i;
      std::string copy(std::string_view(doc.at(1)));
      auto x = result.find(copy);
      if (x == result.end()) {
        result.emplace(copy, brand{double(doc.at(5)) * uint64_t(doc.at(7)), uint64_t(doc.at(7))});
      } else {
        x->second.cumulative_rating += double(doc.at(5)) * uint64_t(doc.at(7));
        x->second.reviews_count += uint64_t(doc.at(7));
      }
    }
    return true;
  }
};

struct amazon_cellphones_ondemand {
  ondemand::parser parser{};
  bool run(padded_string &json, size_t batch_size, std::map<std::string, brand> &result) {
    parser.threaded = true;
    ondemand::document_stream stream = parser.iterate_many(json, batch_size);
    ondemand::document_stream::iterator i = stream.begin();
    ++i;  // Skip first line
    for (; i != stream.end(); ++i) {
      auto doc = *i;
      size_t index{0};
      std::string copy;
      double rating;
      uint64_t reviews;
      for (auto value : doc) {
        switch (index) {
        case 1: copy = std::string(std::string_view(value)); break;
        case 5: rating = double(value); break;
        case 7: reviews = uint64_t(value); break;
        default: break;
        }
        index++;
      }
      auto x = result.find(copy);
      if (x == result.end()) {
        result.emplace(copy, brand{rating * reviews, reviews});
      } else {
        x->second.cumulative_rating += rating * reviews;
        x->second.reviews_count += reviews;
      }
    }
    return true;
  }
};

// large_amazon_cellphones is C++'s build_json(10*1024*1024)
// (benchmark/large_amazon_cellphones/large_amazon_cellphones.h): the file,
// then copies of it without its header line until it spans 10 MiB.
static std::string large_amazon_cellphones(std::string answer) {
  std::string copy(answer, answer.find('\n') + 1);
  while (answer.size() < 10 * 1024 * 1024) { answer.append(copy); }
  return answer;
}

// --- main ---

// task runs one task body under BenchmarkTasks/<name>/<api>, clearing its
// result vector at the start of each run as Go reslices it.
template <typename Impl, typename T>
static void task(const std::string &name, padded_string &json) {
  Impl impl;
  std::vector<T> result;
  bench("Tasks/" + name, json.size(), [&] {
    result.clear();
    if (!impl.run(json, result)) { throw std::runtime_error("run failed"); }
  });
}

template <typename Impl>
static void stream(const std::string &name, padded_string &json, size_t default_batch) {
  for (auto [bs, size] : {std::pair<const char *, size_t>{"default", default_batch}, {"single", json.size()}}) {
    Impl impl;
    std::map<std::string, brand> brands;
    bench(name + "/" + bs, json.size(), [&] { impl.run(json, size, brands); });
  }
}

int main(int argc, char **argv) {
  if (argc < 3) {
    std::fprintf(stderr, "usage: bench CORPUS_DIR GEN_DIR FILE...\n");
    return 2;
  }
  std::string corpus = argv[1], gen = argv[2];

  std::printf("pkg: simdjson-go\n");
  for (int i = 3; i < argc; i++) {
    std::string path = argv[i];
    padded_string json = load(path);
    dom::parser parser;
    bench("Parse/" + path.substr(path.find_last_of('/') + 1), json.size(), [&] {
      if (auto err = parser.parse(json).error()) { throw simdjson_error(err); }
    });
  }
  padded_string small = load(corpus + "/amazon_cellphones.ndjson");
  padded_string large(large_amazon_cellphones(std::string(small.data(), small.size())));
  for (auto *in : {&small, &large}) {
    std::string name = in == &small ? "amazon_cellphones" : "large_amazon_cellphones";
    stream<amazon_cellphones_dom>("ParseMany/" + name, *in, dom::DEFAULT_BATCH_SIZE);
  }

  std::printf("pkg: simdjson-go/ondemand\n");
  padded_string twitter = load(corpus + "/twitter.json");
  padded_string kostya = load(gen + "/kostya.json");
  padded_string large_random = load(gen + "/large_random.json");
  task<partial_tweets_ondemand, tweet>("partial_tweets/ondemand", twitter);
  task<partial_tweets_dom, tweet>("partial_tweets/dom", twitter);
  task<distinct_user_id_ondemand, uint64_t>("distinct_user_id/ondemand", twitter);
  task<distinct_user_id_dom, uint64_t>("distinct_user_id/dom", twitter);
  {
    find_tweet_ondemand od;
    find_tweet_dom dom;
    std::string_view text;
    bench("Tasks/find_tweet/ondemand", twitter.size(), [&] {
      if (!od.run(twitter, 505874901689851904ULL, text)) { throw std::runtime_error("not found"); }
    });
    bench("Tasks/find_tweet/dom", twitter.size(), [&] {
      if (!dom.run(twitter, 505874901689851904ULL, text)) { throw std::runtime_error("not found"); }
    });
  }
  {
    top_tweet_ondemand od;
    top_tweet_dom dom;
    top_tweet_result result;
    bench("Tasks/top_tweet/ondemand", twitter.size(), [&] {
      if (!od.run(twitter, 60, result)) { throw std::runtime_error("none found"); }
    });
    bench("Tasks/top_tweet/dom", twitter.size(), [&] {
      if (!dom.run(twitter, 60, result)) { throw std::runtime_error("none found"); }
    });
  }
  task<kostya_ondemand, point>("kostya/ondemand", kostya);
  task<kostya_dom, point>("kostya/dom", kostya);
  task<large_random_ondemand, point>("large_random/ondemand", large_random);
  task<large_random_dom, point>("large_random/dom", large_random);
  for (auto *in : {&small, &large}) {
    std::string name = in == &small ? "amazon_cellphones" : "large_amazon_cellphones";
    stream<amazon_cellphones_ondemand>("IterateMany/" + name, *in, ondemand::DEFAULT_BATCH_SIZE);
  }
  return 0;
}
