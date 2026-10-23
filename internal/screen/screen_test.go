package screen

import (
	"fmt"
	"strings"
	"testing"
)

// rowText is row y as text (erased cells as spaces, right halves left out).
func rowText(t *Term, y int) string {
	return strings.TrimRight(Text(AppendRow(nil, t.Row(y).C, false)), " ")
}

func histText(t *Term) []string {
	first, next := t.History()
	var out []string
	for id := first; id < next; id++ {
		l, _ := t.HistLine(id)
		s := Text(l.Data)
		if l.Cont {
			s = "+" + s
		}
		out = append(out, s)
	}
	return out
}

func TestPrintWrapAndHistory(t *testing.T) {
	tm := New(5, 2)
	tm.Write([]byte("abcdefg\r\nxy\r\nz"))
	// "abcde" wrapped to "fg", then two line feeds: the first two rows left.
	if got := histText(tm); fmt.Sprint(got) != "[abcde +fg]" {
		t.Fatalf("history %q", got)
	}
	if rowText(tm, 0) != "xy" || rowText(tm, 1) != "z" {
		t.Fatalf("screen %q %q", rowText(tm, 0), rowText(tm, 1))
	}
	first, next := tm.History()
	if first != 0 || next != 2 {
		t.Fatalf("ids %d %d", first, next)
	}
}

func TestWideAndStyles(t *testing.T) {
	tm := New(10, 1)
	tm.Write([]byte("a\x1b[31m日本\x1b[0mb"))
	runs, ok := DecodeRow(AppendRow(nil, tm.Row(0).C, true))
	if !ok || len(runs) != 3 {
		t.Fatalf("runs %+v", runs)
	}
	if runs[1].Width != 2 || runs[1].Text != "日本" || tm.Style(runs[1].Style).FG != PaletteColor|1 {
		t.Fatalf("wide run %+v", runs[1])
	}
	if runs[2].Text != "b" || runs[2].Style != 0 {
		t.Fatalf("last run %+v", runs[2])
	}
	x, _ := tm.Cursor()
	if x != 6 {
		t.Fatalf("cursor x %d", x)
	}
}

func TestCombining(t *testing.T) {
	tm := New(10, 1)
	tm.Write([]byte("éx"))
	runs, _ := DecodeRow(AppendRow(nil, tm.Row(0).C, true))
	if len(runs) != 2 || !runs[0].Single || runs[0].Text != "é" || runs[1].Text != "x" {
		t.Fatalf("runs %+v", runs)
	}
}

func TestReplies(t *testing.T) {
	tm := New(80, 24)
	var got []string
	tm.Reply = func(b []byte) { got = append(got, string(b)) }
	tm.Write([]byte("\x1b[5;7H\x1b[6n\x1b[c\x1b[>c\x1b[?2026$p\x1b[?2026h\x1b[?2026$p\x1b]11;?\x07\x1b[18t"))
	want := []string{"\x1b[5;7R", "\x1b[?1;2c", "\x1b[>0;276;0c", "\x1b[?2026;2$y", "\x1b[?2026;1$y", "\x1b]11;rgb:1111/1414/1919\x1b\\", "\x1b[8;24;80t"}
	if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
		t.Fatalf("replies %q", got)
	}
	if !tm.Modes().Sync {
		t.Fatal("sync mode not set")
	}
}

func TestEvents(t *testing.T) {
	tm := New(10, 2)
	tm.Write([]byte("\x1b]52;c;aGk=\x07\x1b]52;c;?\x07\x1b]6464;open;https://example.com/\x1b\\\x1b]0;title\x07"))
	ev := tm.TakeEvents()
	if len(ev) != 2 || ev[0] != (Event{EventClipboard, "hi"}) || ev[1] != (Event{EventOpen, "https://example.com/"}) {
		t.Fatalf("events %+v", ev)
	}
	if tm.Title() != "title" {
		t.Fatalf("title %q", tm.Title())
	}
	tm.Write([]byte("\x1b]7;file://host/home/dev/my%20work\x07\x1b]7;http://x/y\x07"))
	if tm.Dir() != "/home/dev/my work" {
		t.Fatalf("OSC 7: %q", tm.Dir())
	}
}

func TestLink(t *testing.T) {
	tm := New(20, 1)
	tm.Write([]byte("\x1b]8;;https://a.example/\x1b\\link\x1b]8;;\x1b\\ x"))
	runs, _ := DecodeRow(AppendRow(nil, tm.Row(0).C, true))
	if len(runs) != 2 || tm.Link(tm.Style(runs[0].Style).Link) != "https://a.example/" || runs[1].Style != 0 {
		t.Fatalf("runs %+v", runs)
	}
}

func TestResizeRows(t *testing.T) {
	tm := New(10, 5)
	tm.Write([]byte("1\r\n2\r\n3"))
	// Blank rows below the cursor go first: no history.
	tm.Resize(10, 3)
	if first, next := tm.History(); next-first != 0 || rowText(tm, 2) != "3" {
		t.Fatalf("history %v screen %q", histText(tm), rowText(tm, 2))
	}
	// Then the top rows move to the history.
	tm.Resize(10, 2)
	if fmt.Sprint(histText(tm)) != "[1]" || rowText(tm, 0) != "2" {
		t.Fatalf("history %v screen %q", histText(tm), rowText(tm, 0))
	}
	// Growing adds blank rows at the bottom.
	tm.Resize(4, 4)
	if _, y := tm.Cursor(); y != 1 || rowText(tm, 3) != "" {
		t.Fatalf("cursor row %d", y)
	}
}

func TestClearHistoryKeepsIDs(t *testing.T) {
	tm := New(5, 1)
	tm.Write([]byte("a\r\nb\r\nc"))
	tm.Write([]byte("\x1b[3J"))
	first, next := tm.History()
	if first != 2 || next != 2 {
		t.Fatalf("ids %d %d", first, next)
	}
	tm.Write([]byte("\r\nd"))
	if l, ok := tm.HistLine(2); !ok || Text(l.Data) != "c" {
		t.Fatalf("line 2 %q", l.Data)
	}
}

func TestScrollUpInRegionAtTopKeepsHistory(t *testing.T) {
	tm := New(5, 3)
	tm.Write([]byte("a\r\nb\r\nc\x1b[1;2r\x1b[S"))
	if fmt.Sprint(histText(tm)) != "[a]" {
		t.Fatalf("history %v", histText(tm))
	}
}

func TestHistoryBounds(t *testing.T) {
	tm := New(5, 1)
	tm.MaxHistLines = 3
	for i := 0; i < 10; i++ {
		tm.Write([]byte(fmt.Sprintf("%d\r\n", i)))
	}
	first, next := tm.History()
	if first != 7 || next != 10 || fmt.Sprint(histText(tm)) != "[7 8 9]" {
		t.Fatalf("ids %d %d %v", first, next, histText(tm))
	}
}

func TestLinkTableBounded(t *testing.T) {
	tm := New(20, 2)
	tm.Write([]byte("\x1b]8;;https://kept.example/\x1b\\kept\x1b]8;;\x1b\\\r\n"))
	kept := tm.Style(tm.Row(0).C[0].S).Link
	// Links no cell uses: evicted as the table fills, whatever their number.
	long := strings.Repeat("x", 8000)
	for i := range 5000 {
		fmt.Fprintf(tm, "\x1b]8;;https://e.example/%d/%s\x1b\\\x1b]8;;\x1b\\", i, long)
	}
	if tm.LinkBytes() > maxLinkBytes || tm.Links() > maxLinks {
		t.Fatalf("%d links, %d bytes", tm.Links(), tm.LinkBytes())
	}
	if got := tm.Link(kept); got != "https://kept.example/" {
		t.Fatalf("link on screen became %q", got)
	}
	// An evicted id names no URL; the same URL again gets a new id.
	tm.Write([]byte("\x1b]8;;https://e.example/0/" + long + "\x1b\\a"))
	if id := tm.Style(tm.Row(1).C[0].S).Link; id <= kept+1 || tm.Link(kept+1) != "" {
		t.Fatalf("id %d, old id's URL %q", id, tm.Link(kept+1))
	}
}

func TestBellsAndNotifications(t *testing.T) {
	for _, c := range []struct {
		in     string
		bells  int
		notify []string
	}{
		{"done\a", 1, nil},
		{"\x1b]0;title\a\x1bP1$r\a", 0, nil},
		{"\x1b]9;Build finished\a", 0, []string{"Build finished"}},
		{"\x1b]9;4;1;50\x1b\\", 0, nil},
		{"\x1b]777;notify;Claude Code;Task done\x1b\\", 0, []string{"Claude Code\nTask done"}},
		{"\x1b]777;other;x\a", 0, nil},
		{"\x1b]99;i=1:d=0;Title\x1b\\\x1b]99;i=1:p=body;Body\x1b\\", 0, []string{"Body"}},
		{"\x1b]99;e=1;" + "aGk=" + "\x1b\\\x1b]99;p=?;\x1b\\", 0, []string{"hi"}},
		{"\x1b]9;" + strings.Repeat("é", 3000) + "\a", 0, []string{strings.Repeat("é", 2048)}},
	} {
		tm := New(20, 2)
		tm.Write([]byte(c.in))
		var got []string
		for _, e := range tm.TakeEvents() {
			if e.Kind == EventNotify {
				got = append(got, e.Text)
			}
		}
		if tm.Bells() != c.bells || fmt.Sprint(got) != fmt.Sprint(c.notify) {
			t.Errorf("%q: bells %d, notifications %q", c.in, tm.Bells(), got)
		}
	}
}
