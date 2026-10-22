/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- @board edit / delete / unassign -------------------------------------

// fakeBoardEditor layers the optional BoardEditor extension over the
// plain fake adapter.
type fakeBoardEditor struct{ fakeBoardAdapter }

func (f *fakeBoardEditor) Edit(id, title, description string) (string, error) {
	return f.record("edit", id, title, description)
}
func (f *fakeBoardEditor) Delete(id string) (string, error)   { return f.record("delete", id) }
func (f *fakeBoardEditor) Unassign(id string) (string, error) { return f.record("unassign", id) }

func TestBuiltinBoard_EditDeleteUnassign(t *testing.T) {
	f := &fakeBoardEditor{}
	SetBoardAdapter(f)
	t.Cleanup(func() { SetBoardAdapter(nil) })
	p := NewBuiltinBoardPlugin()

	cases := []struct {
		name     string
		args     []string
		wantCall string
		wantArgs []string
	}{
		{"edit envelope", []string{`{"cmd":"edit","args":{"id":"card-3","title":"T2","description":"D2"}}`}, "edit", []string{"card-3", "T2", "D2"}},
		{"edit argv positional title", []string{"edit", "card-3", "new", "title"}, "edit", []string{"card-3", "new title", ""}},
		{"edit argv flag description", []string{"edit", "card-3", "--description", "D3"}, "edit", []string{"card-3", "", "D3"}},
		{"update alias flat envelope", []string{`{"cmd":"update","id":"card-4","desc":"x"}`}, "edit", []string{"card-4", "", "x"}},
		{"delete envelope", []string{`{"cmd":"delete","args":{"id":"card-3"}}`}, "delete", []string{"card-3"}},
		{"delete argv alias", []string{"rm", "card-5"}, "delete", []string{"card-5"}},
		{"unassign envelope", []string{`{"cmd":"unassign","args":{"card_id":"card-3"}}`}, "unassign", []string{"card-3"}},
		{"unassign argv", []string{"unassign", "card-6"}, "unassign", []string{"card-6"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := p.Execute(context.Background(), c.args)
			require.NoError(t, err)
			assert.Equal(t, "ok:"+c.wantCall, out)
			assert.Equal(t, c.wantCall, f.lastCall)
			assert.Equal(t, c.wantArgs, f.lastArgs)
		})
	}
}

func TestBuiltinBoard_EditValidation(t *testing.T) {
	f := &fakeBoardEditor{}
	SetBoardAdapter(f)
	t.Cleanup(func() { SetBoardAdapter(nil) })
	p := NewBuiltinBoardPlugin()

	_, err := p.Execute(context.Background(), []string{`{"cmd":"edit","args":{"id":"card-3"}}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing to change")

	_, err = p.Execute(context.Background(), []string{`{"cmd":"edit","args":{"title":"x"}}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `missing "id"`)

	_, err = p.Execute(context.Background(), []string{`{"cmd":"delete"}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `missing "id"`)
	assert.Empty(t, f.lastCall, "adapter must not be called when validation fails")
}

func TestBuiltinBoard_EditorUnsupportedByOldAdapter(t *testing.T) {
	withFakeBoardAdapter(t) // plain adapter: no BoardEditor
	p := NewBuiltinBoardPlugin()
	for _, args := range [][]string{
		{`{"cmd":"edit","args":{"id":"card-1","title":"x"}}`},
		{"delete", "card-1"},
		{"unassign", "card-1"},
	} {
		_, err := p.Execute(context.Background(), args)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not supported")
	}
}

func TestBuiltinBoard_NewVerbCaps(t *testing.T) {
	checkCaps(t, "@board", NewBuiltinBoardPlugin(), []capsCase{
		{[]string{`{"cmd":"edit","args":{"id":"card-1","title":"x"}}`}, false, false},
		{[]string{"delete", "card-1"}, false, false},
		{[]string{`{"cmd":"unassign","args":{"id":"card-1"}}`}, false, false},
	})
	p := NewBuiltinBoardPlugin()
	assert.NotEqual(t, p.DescribeCall([]string{"edit", "card-1"}), p.DescribeCall([]string{"delete", "card-1"}))
	for _, verb := range []string{`"edit"`, `"delete"`, `"unassign"`} {
		assert.Contains(t, p.Schema(), verb)
		assert.Contains(t, p.JSONSchema(), verb)
	}
}

// --- @mail peek ------------------------------------------------------------

type fakeMailPeeker struct {
	fakeMailAdapter
	peekFor string
}

func (f *fakeMailPeeker) Peek(recipient string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastCall = "peek"
	f.peekFor = recipient
	return "ok:peek:" + recipient, nil
}

func TestBuiltinMail_Peek(t *testing.T) {
	f := &fakeMailPeeker{}
	SetMailAdapter(f)
	t.Cleanup(func() { SetMailAdapter(nil) })
	p := NewBuiltinMailPlugin()

	cases := []struct {
		args []string
		want string
	}{
		{[]string{`{"cmd":"peek"}`}, "orchestrator"},
		{[]string{`{"cmd":"peek","args":{"for":"coder"}}`}, "coder"},
		{[]string{`{"cmd":"pending","recipient":"reviewer"}`}, "reviewer"},
		{[]string{"peek", "tester"}, "tester"},
		{[]string{"peek", "--for", "coder"}, "coder"},
		{[]string{"peek"}, "orchestrator"},
	}
	for _, c := range cases {
		out, err := p.Execute(context.Background(), c.args)
		require.NoError(t, err, "%v", c.args)
		assert.Equal(t, "ok:peek:"+c.want, out)
		assert.Equal(t, c.want, f.peekFor)
	}

	// Inbox still drains, untouched.
	out, err := p.Execute(context.Background(), []string{`{"cmd":"inbox"}`})
	require.NoError(t, err)
	assert.Equal(t, "ok:inbox", out)

	// history argv keeps working through the shared argv fallback.
	_, err = p.Execute(context.Background(), []string{"history", "--limit", "7"})
	require.NoError(t, err)
	assert.Equal(t, 7, f.lastLimit)
}

func TestBuiltinMail_PeekUnsupportedAndCaps(t *testing.T) {
	withFakeMailAdapter(t)
	p := NewBuiltinMailPlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"peek"}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported")

	checkCaps(t, "@mail", p, []capsCase{
		{[]string{`{"cmd":"peek"}`}, true, true},
		{[]string{"peek", "coder"}, true, true},
		{[]string{`{"cmd":"inbox"}`}, false, false},
	})
	assert.Contains(t, p.DescribeCall([]string{`{"cmd":"peek","args":{"for":"coder"}}`}), "coder")
	assert.Contains(t, p.Schema(), `"peek"`)
	assert.Contains(t, p.JSONSchema(), `"peek"`)
	assert.Contains(t, p.Schema(), "use peek")
}
