"""Write the On-Demand oracle's cases (documents and scripts) as JSON lines.

Run from the repository root: python3 scripts/ondemand-oracle/gen.py > cases.jsonl
Deterministic: the same corpora give the same cases. See README.md.
"""
import json
import os
import random
import sys

R = random.Random(20261007)
READS = ["int64", "uint64", "double", "bool", "null", "string", "type", "ntype", "raw", "walk"]

# Scalars, written as JSON text, including malformed ones.
SCALARS = [
    "0", "-0", "1", "-1", "01", "-01", "1.", "1.5", "-1.5", ".5", "1e", "1e5", "1E+5", "1e-5", "1e+",
    "-", "--1", "+1", "1x", "1.5x", "0.0", "1e309", "-1e309", "1e-400", "4.9e-324", "1.7976931348623157e308",
    "9223372036854775807", "9223372036854775808", "-9223372036854775808", "-9223372036854775809",
    "18446744073709551615", "18446744073709551616", "123456789012345678901234567890",
    "-123456789012345678901234567890", "1e1000000000000000000", "1e10000000000000000000",
    "0.1e-9999999", "3.14159265358979323846264338327950288", "1" + "0" * 400 + "e-400",
    "1" + "0" * 1100, "1." + "1" * 1100, "0." + "0" * 1090 + "1", "0." + "0" * 1070 + "1",
    "true", "false", "null", "tru", "truex", "true1", "fals", "falsex", "nul", "nullx", "nan", "Infinity",
    '""', '"a"', '"a\\u0041"', '"\\u00e9\\u20ac"', '"\\ud83d\\ude00"', '"\\ud800"', '"\\ud800\\u0041"',
    '"\\"\\\\\\/\\b\\f\\n\\r\\t"', '"\\x"', '"\\u12"', '"é😀"', '"a\\"b"',
]

CONTAINERS = [
    "[]", "{}", "[1,2,3]", '{"a":1,"b":2}', '{"a":{"b":[1,{"c":2}]}}', "[[[[]]]]", '[{"a":[]},{"a":{}}]',
    '{"a":1,"a":2}', '{"\\u0061":1,"a":2}', '{"a\\"b":1}', '{"":1}', '{"a":1,"b":2,"c":3,"d":4}',
    '{"x":[1,2,3],"y":{"z":null},"w":"s"}', ' { "a" : [ 1 , 2 ] , "b" : true } ', "[1,]", "[,1]", "[1 2]",
    '{"a" 1}', '{"a":1,}', '{,"a":1}', '{"a":1 "b":2}', "{1:2}", "[}", "{]", "[[]", "[]]", "{}}", '{"a":[}',
    "[1,{]", '{"a":{"b":1}', "[", "{", "]", "}", ",", ":", '{"a"', '{"a":', "[1", "[1,", '["a"',
    '{"a":1}x', "[1]x", "[] []", "{} 1", "\ufeff[1]", "  [1]  ", "[1]\n", "[\"\\u0000\"]", '{"a":"b"}',
    '[true,false,null]', '[truex]', '[nul]', '{"a":tru}', "[1.5e400]", "[-]",
    '{"a":[1,2],"b":{"c":3},"d":4}', '{"user":{"id":1,"name":"x"},"id":2}',
]

FILES_SKIP = {"numbers.json"}  # too long a walk to be useful; covered by DOM agreement


def case(doc=None, file=None, script=()):
    c = {"script": list(script)}
    if file is not None:
        c["file"] = file
    else:
        c["doc"] = doc.encode("utf-8") if isinstance(doc, str) else doc
        c["doc"] = c["doc"].hex()
    return c


def scalar_cases():
    for s in SCALARS:
        for op in READS:
            yield case(s, script=[op])
        yield case(s + " ", script=["int64"])
        for pad in (19, 20, 21, 1082, 1083):  # C++ copies a root scalar with its trailing space
            for op in ("int64", "uint64", "double", "ntype", "bool", "null", "walk"):
                yield case(s + " " * pad, script=[op])
        yield case(s + " 1", script=["double"])
        yield case(s + " 1", script=["string"])
        yield case(s, script=["val"])
        for wrap, path in (("[%s]", ["arr", "at 0"]), ("{\"a\":%s}", ["get a"]),
                           ("[%s,1]", ["arr", "at 0"]), ("{\"a\":%s,\"b\":1}", ["find a"]),
                           ("[0,%s]", ["arr", "at 1"]), ("{\"b\":0,\"a\":%s}", ["get a"])):
            doc = wrap % s
            yield case(doc, script=["walk"])
            for op in READS:
                yield case(doc, script=path + [op])
            # A scalar read after the cursor moved on (C++ allows it).
            if wrap.startswith("{"):
                yield case(doc, script=path + ["get b", "int64", "pop", "double"])


def container_scripts(doc):
    s = [["walk"], ["raw"], ["type"], ["val", "walk"], ["obj", "keys"], ["arr", "each"],
         ["obj", "count", "keys"], ["arr", "count", "each"], ["arr", "count", "count"],
         ["obj", "count", "reset", "keys"], ["arr", "at 0", "walk"], ["arr", "at 1", "walk"],
         ["arr", "at 5"], ["obj", "raw"], ["arr", "raw"], ["ptr /a"], ["ptr /0"], ["ptr /a/b"],
         ["ptr /a/b/1/c", "walk"], ["ptr /x/1", "walk"], ["ptr /y/z", "null"], ["ptr a"], ["ptr /~2"],
         ["ptr /-"], ["ptr /01"], ["ptr "], ["get a", "walk"], ["find a", "walk"], ["get b", "walk", "get a", "walk"],
         ["find b", "walk", "find a"], ["get missing"], ["get a", "get b", "walk"], ["find a", "find b", "walk"],
         ["get \\u0061", "walk"], ["get a\\\"b", "walk"], ["get ", "walk"], ["obj", "get d", "walk", "get a", "walk"],
         ["get a", "rewind", "get b", "walk"], ["walk", "rewind", "walk"], ["get user", "find id", "int64", "pop", "find name", "string"],
         ["get id", "int64", "get user", "walk"], ["arr", "at 0", "arr", "each"],
         ["get a", "ptr ", "raw"], ["get a", "ptr ", "walk"], ["ptr /a/b/1/c/d"], ["ptr /a/b/1/c/~"],
         ["ptr /a/b/01"], ["ptr /a/b/-"], ["ptr /a/b/x"], ["ptr /a/"], ["ptr /0/"], ["ptr /0/0/0/0"]]
    return s


def long_name_cases():
    # Names longer than SIMDJSON_PADDING (64) are compared with is_equal,
    # which stops at an unescaped quote in the name; shorter ones are not.
    a, b = "a" * 40, "b" * 30
    docs = ['{"%s":1,"%s":2}' % (a, b), '{"a":1,"b":2}', '{"%s":1}' % ("k" * 70),
            '{"%s\\"b":1}' % ("q" * 70), '{"%s\\\\":1,"x":2}' % ("w" * 70)]
    names = ['%s":1,"%s' % (a, b), 'a":1,"b', "k" * 70, "k" * 69, "q" * 70 + '\\"b',
             "w" * 70 + '\\\\":1,"x', "w" * 70 + "\\\\"]
    for d in docs:
        for n in names:
            for op in ("get", "find"):
                yield case(d, script=[op + " " + n, "walk"])


def container_cases():
    for d in CONTAINERS:
        for script in container_scripts(d):
            yield case(d, script=script)


def pointers(v, prefix="", out=None):
    out = [] if out is None else out
    out.append(prefix)
    if isinstance(v, dict):
        for k, x in v.items():
            pointers(x, prefix + "/" + k.replace("~", "~0").replace("/", "~1"), out)
    elif isinstance(v, list):
        for i, x in enumerate(v):
            pointers(x, prefix + "/" + str(i), out)
    return out


def corpus_cases():
    for sub in ("jsonexamples", "jsonchecker"):
        base = os.path.join("testdata", sub)
        for name in sorted(os.listdir(base)):
            if not name.endswith(".json") or name in FILES_SKIP:
                continue
            rel = os.path.join(base, name)
            for script in (["walk"], ["type"], ["raw"], ["val", "walk"], ["obj", "keys"], ["arr", "each"],
                           ["obj", "count"], ["arr", "count"]):
                yield case(file=rel, script=script)
            raw = open(rel, "rb").read()
            try:
                v = json.loads(raw)
            except Exception:
                continue
            ptrs = [p for p in pointers(v) if p]
            # Pointers C++ cannot follow (escaped names) are compared as errors.
            for p in R.sample(ptrs, min(40, len(ptrs))):
                yield case(file=rel, script=["ptr " + p, "walk"])
            if isinstance(v, dict) and v:
                keys = list(v)
                for _ in range(5):
                    pick = R.sample(keys, min(3, len(keys)))
                    script = []
                    for k in pick:
                        script += ["get " + k, "type", "get " + k, "walk"][0:1] + ["walk"]
                    yield case(file=rel, script=script)
                    yield case(file=rel, script=sum((["find " + k, "walk"] for k in keys[:3]), []))


def mutate(doc):
    b = bytearray(doc.encode("utf-8"))
    kind = R.randrange(4)
    i = R.randrange(len(b) + 1)
    if kind == 0:
        return bytes(b[:i])  # truncate
    if kind == 1 and b:
        b[min(i, len(b) - 1)] = ord(R.choice(']},:x1" t[{n'))
    elif kind == 2:
        b.insert(i, ord(R.choice(']},:x1" t[{"')))
    elif b:
        del b[min(i, len(b) - 1)]
    return bytes(b)


def mutation_cases():
    seeds = [d for d in CONTAINERS if len(d) > 4] + ['{"a":[1,2.5,"x",true,null],"b":{"c":-3},"d":"e"}']
    for _ in range(4000):
        d = mutate(R.choice(seeds))
        for script in (["walk"], ["obj", "keys"], ["arr", "each"], ["get a", "walk"], ["get b", "walk", "get d", "walk"],
                       ["ptr /a/1", "walk"], ["obj", "count"], ["raw"]):
            yield case(d, script=script)


RS_ = random.Random(20261008)  # separate, so the On-Demand cases stay byte-identical
SFORMATS = ["whitespace", "newline", "sequence", "comma", "array"]
SDOCS = ["1", "-2.5", "1e400", '"a\\"b"', '"é"', "true", "nul", "null", "[]", "{}", "[1,[2,{\"a\":3}]]",
         '{"a":{"b":[1,2]},"c":"x"}', '{"a":1,"b":[true,null]}', "[1,", '{"a":', "]", "}", '"open',
         "[1 2]", "[1,23", "18446744073709551616", '{"a":1,"a":2}']
SSCRIPTS = [[], ["walk"], ["get a", "walk"], ["find b", "walk"], ["type"], ["obj", "keys"],
            ["arr", "each"], ["raw"], ["int64"], ["ptr /a/b/1", "walk"]]
PINNED = [
    (" 1111 }", "whitespace"), ('[1,23] "lone string" {"key":"unfinished value}', "whitespace"),
    ('{"a":1},{"b":', "comma"), ('1,2,"abc', "comma"), ("\x1e1\n\x1e2\n\x1e \"abc", "sequence"),
    ("\x1e\x1e1\n\x1e\x1e\x1e2\n\x1e", "sequence"), (',1,,2,,"x",,', "comma"), ("true  {  ", "whitespace"),
    ("1 2 34", "whitespace"), ("\x1e\x1e \x1e", "sequence"), ("[1,23 [1,23]", "whitespace"),
    ("[1,23 [1,23] [1,23 [1,23]", "whitespace"), ("\ufeff[1] [2]", "whitespace"),
    ('  [ {"a":1} , 2 ]  ', "array"), ("[]", "array"), ("[ ]", "array"), ("{}", "array"), ("[", "array"),
    ('[{"a":1},2]]', "array"), ('{"id":1}\n12\n"a,\\"b"', "comma"), ("{\"a\":1}\r\n{\"a\":2}", "newline"),
    ('{"a":1,\n"b":2}\n[3]', "newline"), ("\x1e1\x1e2", "sequence"), ('\x1e"abc"\x1e[1]', "sequence"),
    ("1\n2\n", "newline"), ("", "whitespace"), ("   ", "whitespace"),
]


def scase(doc=None, file=None, fmt="whitespace", api="dom", script=()):
    c = case(doc, file, script)
    c["stream"] = {"api": api, "format": fmt}
    return c


def joined(docs, fmt):
    if fmt == "array":
        return "[" + ",".join(docs) + "]"
    if fmt == "comma":
        return RS_.choice([",", " , ", ",\n"]).join(docs)
    if fmt == "sequence":
        return "".join("\x1e" + d + RS_.choice(["", "\n", " "]) for d in docs)
    if fmt == "newline":
        return RS_.choice(["\n", "\r\n"]).join(docs)
    return "".join(d + RS_.choice([" ", "\n", ""]) for d in docs)


def stream_cases():
    for doc, fmt in PINNED:
        yield scase(doc, fmt=fmt)
        for script in ([], ["walk"]):
            yield scase(doc, fmt=fmt, api="ondemand", script=script)
    for _ in range(600):
        fmt = RS_.choice(SFORMATS)
        doc = joined([RS_.choice(SDOCS) for _ in range(RS_.randint(1, 8))], fmt)
        yield scase(doc, fmt=fmt)
        yield scase(doc, fmt=fmt, api="ondemand", script=RS_.choice(SSCRIPTS))
    for _ in range(40):  # every byte prefix: the trim at every possible end
        fmt = RS_.choice(["whitespace", "comma", "sequence"])
        doc = joined([RS_.choice(SDOCS[:13]) for _ in range(3)], fmt)
        for k in range(len(doc) + 1):
            yield scase(doc[:k], fmt=fmt)
            yield scase(doc[:k], fmt=fmt, api="ondemand", script=["walk"])
    files = sorted(f for f in os.listdir("testdata/jsonexamples") if f.endswith((".json", ".ndjson")))
    for f in files:
        if f in FILES_SKIP:
            continue
        path = "testdata/jsonexamples/" + f
        for fmt in (["whitespace", "newline"] if f.endswith(".ndjson") else ["whitespace"]):
            yield scase(file=path, fmt=fmt)
            yield scase(file=path, fmt=fmt, api="ondemand", script=["walk"])


def main():
    seen = set()
    for gen in (scalar_cases, container_cases, long_name_cases, corpus_cases, mutation_cases, stream_cases):
        for c in gen():
            line = json.dumps(c, ensure_ascii=False, sort_keys=True)
            if line not in seen:
                seen.add(line)
                sys.stdout.write(line + "\n")


main()
