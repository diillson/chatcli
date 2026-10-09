package sum

import "testing"

func TestSum(t *testing.T) {
	cases := []struct {
		in   []int
		want int
	}{
		{nil, 0},
		{[]int{5}, 5},
		{[]int{1, 2, 3}, 6},
		{[]int{-1, 1, 10}, 10},
	}
	for _, c := range cases {
		if got := Sum(c.in); got != c.want {
			t.Errorf("Sum(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestMax(t *testing.T) {
	if got := Max([]int{3, 9, 2}); got != 9 {
		t.Errorf("Max = %d, want 9", got)
	}
}
