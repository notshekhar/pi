// Package build carries facts about this binary that more than one place has
// to agree on.
package build

// Version is what pi reports about itself: the masthead, and the client
// version handed to every MCP server at handshake.
//
// One constant because it was two. The MCP handshake had its own copy of the
// string, which is the kind of duplicate that is correct until the first
// release that forgets it and then silently tells every server the wrong
// thing for a version or two.
const Version = "0.2.1"
