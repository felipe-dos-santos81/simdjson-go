// The On-Demand oracle: runs scripted reads through C++ simdjson ondemand
// and prints what each read returns, for ondemand/oracle_test.go to replay.
// See README.md in this directory. Built against the simdjson v5.0.2
// single-header release (simdjson.h, simdjson.cpp) in release mode.
//
// Input, one JSON object per line on stdin:
//   {"doc":"<hex bytes>","script":["op",...]}  or  {"file":"<path>","script":[...]}
// Output, one line per case: the script's output, as a JSON string.
//
// The script works on a stack of handles; the document is at the bottom.
//   get K / find K   look field K up on the top (document, value or object):
//                    unordered (operator[]) / ordered (find_field); push the value
//   obj / arr / val  replace the top with it as an object / array / value
//   at I             push element I of the top array
//   ptr P            push the value at JSON Pointer P below the top
//   pop              drop the top
//   int64 uint64 double bool null string type ntype raw
//                    read the top (document or value) that way, print, pop
//   count / reset    count / reset the top object or array (it stays)
//   rewind           rewind the document; drop everything above it
//   keys             iterate the top object, print each raw key, skip values; pop
//   each             iterate the top array without reading elements, print the count; pop
//   walk             read the top (document or value) completely, print it; pop
// An error prints !<code> and ends the script. Output tokens are joined by
// spaces; output over 2000 bytes is replaced by #<fnv1a64>:<length>.
//
// A stream case adds "stream":{"api":"dom"|"ondemand","format":F}, F one of
// whitespace newline sequence comma array, and runs the input through
// parse_many or iterate_many in one batch. Its output, per document:
//   @<current_index> h<hex source>, then w<walk_dom> (dom) or the script's
//   tokens on the document, plus x<code> if a read failed, then "dead" and
//   nothing more if that read abandoned the document (ondemand)
// and at the end !<code> if the stream failed or ~<truncated_bytes> if not.
// An up-front comma_delimited_array failure prints only !3.
#include "simdjson.h"
#include <cinttypes>
#include <cstdio>
#include <fstream>
#include <iostream>
#include <sstream>
#include <string>
#include <vector>

using namespace simdjson;

static std::string esc(std::string_view s) {
  std::string out;
  for (unsigned char c : s) {
    if (c > 0x20 && c < 0x7f && c != '\\') {
      out += char(c);
    } else {
      char b[8];
      snprintf(b, sizeof b, "\\x%02x", c);
      out += b;
    }
  }
  return out;
}

static std::string trim_ws(std::string_view s) {
  while (!s.empty() && (s.back() == ' ' || s.back() == '\t' || s.back() == '\n' || s.back() == '\r')) {
    s.remove_suffix(1);
  }
  return std::string(s);
}

static std::string f64(double d) {
  uint64_t bits;
  memcpy(&bits, &d, 8);
  char b[32];
  snprintf(b, sizeof b, "d%016" PRIx64, bits);
  return b;
}

static const char *type_name(ondemand::json_type t) {
  switch (t) {
    case ondemand::json_type::array: return "array";
    case ondemand::json_type::object: return "object";
    case ondemand::json_type::number: return "number";
    case ondemand::json_type::string: return "string";
    case ondemand::json_type::boolean: return "bool";
    case ondemand::json_type::null: return "null";
    default: return "unknown";
  }
}

static const char *ntype_name(ondemand::number_type t) {
  switch (t) {
    case ondemand::number_type::signed_integer: return "int64";
    case ondemand::number_type::unsigned_integer: return "uint64";
    case ondemand::number_type::floating_point_number: return "float64";
    case ondemand::number_type::big_integer: return "bigint";
  }
  return "?";
}

struct out_t {
  std::vector<std::string> toks;
  void add(std::string s) { toks.push_back(std::move(s)); }
};

#define TRY(x) do { error_code _e = (x); if (_e) { return _e; } } while (0)

template <typename V> static error_code walk(V &v, std::string &out);

template <typename V> static error_code walk_object(ondemand::object o, std::string &out) {
  out += '{';
  for (auto f : o) {
    ondemand::field field;
    TRY(std::move(f).get(field));
    out += esc(field.escaped_key());
    out += ':';
    ondemand::value v = field.value();
    TRY(walk(v, out));
    out += ',';
  }
  out += '}';
  return SUCCESS;
}

template <typename V> static error_code walk_array(ondemand::array a, std::string &out) {
  out += '[';
  for (auto e : a) {
    ondemand::value v;
    TRY(e.get(v));
    TRY(walk(v, out));
    out += ',';
  }
  out += ']';
  return SUCCESS;
}

// walk reads v completely (V is ondemand::value or ondemand::document).
template <typename V> static error_code walk(V &v, std::string &out) {
  ondemand::json_type t;
  TRY(v.type().get(t));
  switch (t) {
    case ondemand::json_type::object: {
      ondemand::object o;
      TRY(v.get_object().get(o));
      return walk_object<V>(o, out);
    }
    case ondemand::json_type::array: {
      ondemand::array a;
      TRY(v.get_array().get(a));
      return walk_array<V>(a, out);
    }
    case ondemand::json_type::number: {
      ondemand::number_type nt;
      TRY(v.get_number_type().get(nt));
      switch (nt) {
        case ondemand::number_type::signed_integer: {
          int64_t x;
          TRY(v.get_int64().get(x));
          out += "i" + std::to_string(x);
          return SUCCESS;
        }
        case ondemand::number_type::unsigned_integer: {
          uint64_t x;
          TRY(v.get_uint64().get(x));
          out += "u" + std::to_string(x);
          return SUCCESS;
        }
        case ondemand::number_type::floating_point_number: {
          double x;
          TRY(v.get_double().get(x));
          out += f64(x);
          return SUCCESS;
        }
        case ondemand::number_type::big_integer: {
          std::string_view r;
          TRY(v.raw_json().get(r));
          out += "B" + esc(trim_ws(r));
          return SUCCESS;
        }
      }
      return SUCCESS;
    }
    case ondemand::json_type::string: {
      std::string_view s;
      TRY(v.get_string().get(s));
      out += "s" + esc(s);
      return SUCCESS;
    }
    case ondemand::json_type::boolean: {
      bool b;
      TRY(v.get_bool().get(b));
      out += b ? "true" : "false";
      return SUCCESS;
    }
    case ondemand::json_type::null: {
      bool n;
      TRY(v.is_null().get(n));
      out += n ? "null" : "notnull";
      return SUCCESS;
    }
    default:
      out += "?";
      return SUCCESS;
  }
}

enum kind { K_DOC, K_VALUE, K_OBJECT, K_ARRAY };

struct slot {
  kind k;
  ondemand::value v;
  ondemand::object o;
  ondemand::array a;
};

template <typename V> static error_code read(V &v, const std::string &op, out_t &out) {
  if (op == "int64") { int64_t x; TRY(v.get_int64().get(x)); out.add("i" + std::to_string(x)); }
  else if (op == "uint64") { uint64_t x; TRY(v.get_uint64().get(x)); out.add("u" + std::to_string(x)); }
  else if (op == "double") { double x; TRY(v.get_double().get(x)); out.add(f64(x)); }
  else if (op == "bool") { bool x; TRY(v.get_bool().get(x)); out.add(x ? "true" : "false"); }
  else if (op == "null") { bool x; TRY(v.is_null().get(x)); out.add(x ? "null" : "notnull"); }
  else if (op == "string") { std::string_view x; TRY(v.get_string().get(x)); out.add("s" + esc(x)); }
  else if (op == "type") { ondemand::json_type x; TRY(v.type().get(x)); out.add(type_name(x)); }
  else if (op == "ntype") { ondemand::number_type x; TRY(v.get_number_type().get(x)); out.add(ntype_name(x)); }
  else if (op == "raw") { std::string_view x; TRY(v.raw_json().get(x)); out.add("r" + esc(x)); }
  else if (op == "walk") { std::string s; TRY(walk(v, s)); out.add(s); }
  else { return UNEXPECTED_ERROR; }
  return SUCCESS;
}

template <typename D> static error_code run(D &doc, const std::vector<std::string> &script, out_t &out) {
  std::vector<slot> st;
  st.push_back(slot{K_DOC, {}, {}, {}});
  for (const std::string &line : script) {
    std::string op = line, arg;
    size_t sp = line.find(' ');
    if (sp != std::string::npos) { op = line.substr(0, sp); arg = line.substr(sp + 1); }
    slot &top = st.back();
    if (op == "get" || op == "find") {
      ondemand::value v;
      bool ordered = op == "find";
      switch (top.k) {
        case K_DOC: TRY((ordered ? doc.find_field(arg) : doc.find_field_unordered(arg)).get(v)); break;
        case K_VALUE: TRY((ordered ? top.v.find_field(arg) : top.v.find_field_unordered(arg)).get(v)); break;
        case K_OBJECT: TRY((ordered ? top.o.find_field(arg) : top.o.find_field_unordered(arg)).get(v)); break;
        default: return INCORRECT_TYPE;
      }
      st.push_back(slot{K_VALUE, v, {}, {}});
    } else if (op == "obj") {
      ondemand::object o;
      if (top.k == K_DOC) { TRY(doc.get_object().get(o)); }
      else if (top.k == K_VALUE) { TRY(top.v.get_object().get(o)); }
      else { return INCORRECT_TYPE; }
      if (top.k == K_DOC) { st.push_back(slot{K_OBJECT, {}, o, {}}); } else { top = slot{K_OBJECT, {}, o, {}}; }
    } else if (op == "arr") {
      ondemand::array a;
      if (top.k == K_DOC) { TRY(doc.get_array().get(a)); }
      else if (top.k == K_VALUE) { TRY(top.v.get_array().get(a)); }
      else { return INCORRECT_TYPE; }
      if (top.k == K_DOC) { st.push_back(slot{K_ARRAY, {}, {}, a}); } else { top = slot{K_ARRAY, {}, {}, a}; }
    } else if (op == "val") {
      ondemand::value v;
      TRY(doc.get_value().get(v));
      st.push_back(slot{K_VALUE, v, {}, {}});
    } else if (op == "at") {
      if (top.k != K_ARRAY) { return INCORRECT_TYPE; }
      ondemand::value v;
      TRY(top.a.at(std::stoul(arg)).get(v));
      st.push_back(slot{K_VALUE, v, {}, {}});
    } else if (op == "ptr") {
      ondemand::value v;
      switch (top.k) {
        case K_DOC: TRY(doc.at_pointer(arg).get(v)); break;
        case K_VALUE: TRY(top.v.at_pointer(arg).get(v)); break;
        case K_OBJECT: TRY(top.o.at_pointer(arg).get(v)); break;
        case K_ARRAY: TRY(top.a.at_pointer(arg).get(v)); break;
      }
      st.push_back(slot{K_VALUE, v, {}, {}});
    } else if (op == "pop") {
      if (st.size() > 1) { st.pop_back(); }
    } else if (op == "count") {
      size_t n;
      if (top.k == K_OBJECT) { TRY(top.o.count_fields().get(n)); }
      else if (top.k == K_ARRAY) { TRY(top.a.count_elements().get(n)); }
      else { return INCORRECT_TYPE; }
      out.add("n" + std::to_string(n));
    } else if (op == "reset") {
      bool b;
      if (top.k == K_OBJECT) { TRY(top.o.reset().get(b)); }
      else if (top.k == K_ARRAY) { TRY(top.a.reset().get(b)); }
      else { return INCORRECT_TYPE; }
      out.add("ok");
    } else if (op == "rewind") {
      doc.rewind();
      st.resize(1);
    } else if (op == "keys") {
      if (top.k != K_OBJECT) { return INCORRECT_TYPE; }
      std::string s;
      for (auto f : top.o) {
        ondemand::field field;
        TRY(std::move(f).get(field));
        s += esc(field.escaped_key()) + ",";
      }
      out.add("k" + s);
      st.pop_back();
    } else if (op == "each") {
      if (top.k != K_ARRAY) { return INCORRECT_TYPE; }
      size_t n = 0;
      for (auto e : top.a) {
        ondemand::value v;
        TRY(e.get(v));
        n++;
      }
      out.add("e" + std::to_string(n));
      st.pop_back();
    } else {
      error_code e = top.k == K_DOC ? read(doc, op, out) : top.k == K_VALUE ? read(top.v, op, out) : INCORRECT_TYPE;
      if (top.k == K_OBJECT && op == "raw") {
        std::string_view x;
        TRY(top.o.raw_json().get(x));
        out.add("r" + esc(x));
        e = SUCCESS;
      } else if (top.k == K_ARRAY && op == "raw") {
        std::string_view x;
        TRY(top.a.raw_json().get(x));
        out.add("r" + esc(x));
        e = SUCCESS;
      }
      TRY(e);
      if (st.size() > 1) { st.pop_back(); }
    }
  }
  return SUCCESS;
}

static uint64_t fnv1a(const std::string &s) {
  uint64_t h = 14695981039346656037ull;
  for (unsigned char c : s) { h ^= c; h *= 1099511628211ull; }
  return h;
}

static std::string json_quote(const std::string &s) {
  std::string out = "\"";
  for (unsigned char c : s) {
    if (c == '"' || c == '\\') { out += '\\'; out += char(c); }
    else if (c < 0x20) { char b[8]; snprintf(b, sizeof b, "\\u%04x", c); out += b; }
    else { out += char(c); }
  }
  return out + "\"";
}

static std::string unhex(std::string_view h) {
  std::string out;
  for (size_t i = 0; i + 1 < h.size(); i += 2) {
    out += char(std::stoi(std::string(h.substr(i, 2)), nullptr, 16));
  }
  return out;
}

static std::string tohex(std::string_view s) {
  static const char *digits = "0123456789abcdef";
  std::string out;
  for (unsigned char c : s) { out += digits[c >> 4]; out += digits[c & 15]; }
  return out;
}

// walk_dom prints a DOM value in walk's format (DOM keys are unescaped).
static void walk_dom(dom::element e, std::string &out) {
  switch (e.type()) {
    case dom::element_type::ARRAY: {
      dom::array a = e.get_array().value_unsafe(); // a named copy: the result is a temporary
      out += '[';
      for (dom::element x : a) { walk_dom(x, out); out += ','; }
      out += ']';
      break;
    }
    case dom::element_type::OBJECT: {
      dom::object o = e.get_object().value_unsafe();
      out += '{';
      for (dom::key_value_pair kv : o) {
        out += esc(kv.key); out += ':'; walk_dom(kv.value, out); out += ',';
      }
      out += '}';
      break;
    }
    case dom::element_type::INT64: out += "i" + std::to_string(e.get_int64().value_unsafe()); break;
    case dom::element_type::UINT64: out += "u" + std::to_string(e.get_uint64().value_unsafe()); break;
    case dom::element_type::DOUBLE: out += f64(e.get_double().value_unsafe()); break;
    case dom::element_type::STRING: out += "s" + esc(e.get_string().value_unsafe()); break;
    case dom::element_type::BOOL: out += e.get_bool().value_unsafe() ? "true" : "false"; break;
    case dom::element_type::NULL_VALUE: out += "null"; break;
    default: out += "?"; break;
  }
}

static stream_format parse_format(std::string_view f) {
  if (f == "newline") { return stream_format::newline_delimited; }
  if (f == "sequence") { return stream_format::json_sequence; }
  if (f == "comma") { return stream_format::comma_delimited; }
  if (f == "array") { return stream_format::comma_delimited_array; }
  return stream_format::whitespace_delimited;
}

// clip bounds a source to the input: C++ can end one byte into the padding.
static std::string_view clip(std::string_view s, const padded_string &json) {
  size_t room = size_t(json.data() + json.size() - s.data());
  return s.substr(0, std::min(s.size(), room));
}

static dom::parser dom_stream_parser;
static ondemand::parser od_stream_parser;

// run_stream runs a stream in one batch (batch_size >= len, so no thread).
static void run_stream(const std::string &input, std::string_view api, std::string_view fmt,
                       const std::vector<std::string> &script, out_t &out) {
  padded_string json(input);
  size_t batch = std::max<size_t>(input.size(), 32);
  stream_format f = parse_format(fmt);
  if (api == "dom") {
    dom::document_stream s;
    error_code err = dom_stream_parser.parse_many(json.data(), json.size(), batch, f).get(s);
    if (err) { out.add("!" + std::to_string(int(err))); return; }
    for (auto it = s.begin(); it != s.end(); ++it) {
      out.add("@" + std::to_string(it.current_index()));
      dom::element e;
      if ((err = (*it).get(e))) { out.add("!" + std::to_string(int(err))); return; }
      out.add("h" + tohex(clip(it.source(), json)));
      std::string w;
      walk_dom(e, w);
      out.add("w" + w);
    }
    out.add("~" + std::to_string(s.truncated_bytes()));
    return;
  }
  ondemand::document_stream s;
  error_code err = od_stream_parser.iterate_many(json.data(), json.size(), batch, f).get(s);
  if (err) { out.add("!" + std::to_string(int(err))); return; }
  for (auto it = s.begin(); it != s.end(); ++it) {
    out.add("@" + std::to_string(it.current_index()));
    ondemand::document_reference d;
    if ((err = (*it).get(d))) { out.add("!" + std::to_string(int(err))); return; }
    out.add("h" + tohex(clip(it.source(), json)));
    if ((err = run(d, script, out))) { out.add("x" + std::to_string(int(err))); }
    // A read that abandoned the document leaves C++ with a null parser,
    // which the next step can dereference: nothing past it is defined.
    if (!static_cast<ondemand::document &>(d).is_alive()) { out.add("dead"); return; }
  }
  out.add("~" + std::to_string(s.truncated_bytes()));
}

int main() {
  dom::parser cases;
  ondemand::parser parser;
  std::string line;
  while (std::getline(std::cin, line)) {
    dom::element c;
    if (cases.parse(line).get(c)) { std::cerr << "bad case line\n"; return 1; }
    std::string input;
    std::string_view s;
    if (!c["doc"].get(s)) {
      input = unhex(s);
    } else if (!c["file"].get(s)) {
      std::ifstream f{std::string(s), std::ios::binary};
      std::stringstream ss;
      ss << f.rdbuf();
      input = ss.str();
    }
    std::vector<std::string> script;
    for (auto op : c["script"].get_array()) { script.push_back(std::string(std::string_view(op))); }
    out_t out;
    dom::element stc;
    if (!c["stream"].get(stc)) {
      std::string_view api, fmt;
      (void)stc["api"].get(api);
      (void)stc["format"].get(fmt);
      run_stream(input, api, fmt, script, out);
    } else {
      padded_string json(input);
      ondemand::document doc;
      error_code err = parser.iterate(json).get(doc);
      if (!err) { err = run(doc, script, out); }
      if (err) { out.add("!" + std::to_string(int(err))); }
    }
    std::string joined;
    for (size_t i = 0; i < out.toks.size(); i++) { joined += (i ? " " : "") + out.toks[i]; }
    if (joined.size() > 2000) {
      char b[48];
      snprintf(b, sizeof b, "#%016" PRIx64 ":%zu", fnv1a(joined), joined.size());
      joined = b;
    }
    std::cout << json_quote(joined) << "\n";
  }
}
