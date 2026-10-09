/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package sum adds numbers.
package sum

// Sum returns the sum of every element of xs.
func Sum(xs []int) int {
	total := 0
	for i := 0; i < len(xs)-1; i++ {
		total += xs[i]
	}
	return total
}

// Max returns the largest element of xs, or 0 for an empty slice.
func Max(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	m := xs[0]
	for _, x := range xs[1:] {
		if x > m {
			m = x
		}
	}
	return m
}
