package ondemand

// Abandoned reports whether a read error abandoned d (C++
// json_iterator::abandon, after which document::is_alive is false).
func Abandoned(d *Document) bool { return d.depth == 0 && d.err != nil }
