package tui

import "testing"

func TestParseDiff_ThreeFiles(t *testing.T) {
	// Minimal but realistic shape; ANSI escapes optional and stripped from
	// the header capture by the regex.
	out := `diff --git a/foo.go b/foo.go
index aaa..bbb 100644
--- a/foo.go
+++ b/foo.go
@@ -1 +1 @@
-old
+new
diff --git a/bar/baz.txt b/bar/baz.txt
new file mode 100644
index 000..ccc
--- /dev/null
+++ b/bar/baz.txt
@@ -0,0 +1 @@
+hello
diff --git a/deleted.md b/deleted.md
deleted file mode 100644
index ddd..000
--- a/deleted.md
+++ /dev/null
@@ -1 +0,0 @@
-bye
`
	s := parseDiff(out)
	if got := len(s.Files); got != 3 {
		t.Fatalf("Files = %d, want 3", got)
	}
	wantPaths := []string{"foo.go", "bar/baz.txt", "deleted.md"}
	for i, p := range wantPaths {
		if s.Files[i].Path != p {
			t.Errorf("Files[%d].Path = %q, want %q", i, s.Files[i].Path, p)
		}
		if !s.Files[i].Keep {
			t.Errorf("Files[%d].Keep = false, want default true", i)
		}
	}
}

func TestParseDiff_Empty(t *testing.T) {
	if s := parseDiff(""); len(s.Files) != 0 {
		t.Errorf("empty input produced %d files", len(s.Files))
	}
}

func TestParseDiff_SelectedAndDiscarded(t *testing.T) {
	out := `diff --git a/a b/a
@@ -1 +1 @@
-x
+y
diff --git a/b b/b
@@ -1 +1 @@
-x
+y
`
	s := parseDiff(out)
	s.Files[0].Keep = false
	if got := s.DiscardedFiles(); len(got) != 1 || got[0] != "a" {
		t.Errorf("DiscardedFiles = %v, want [a]", got)
	}
	if got := s.SelectedFiles(); len(got) != 1 || got[0] != "b" {
		t.Errorf("SelectedFiles = %v, want [b]", got)
	}
}
