package main

import (
	"iter"
	"strconv"
	"testing"
)

func TestRandomIDReturnsDistinctExplicitIDs(t *testing.T) {
	first, err := randomID()
	if err != nil || first == "" {
		t.Fatalf("randomID() = %q, %v", first, err)
	}
	second, err := randomID()
	if err != nil || second == "" || second == first {
		t.Fatalf("randomID() returned duplicate or invalid IDs: %q, %q, %v", first, second, err)
	}
}

func TestTaskARNBatchesRespectDescribeLimit(t *testing.T) {
	arns := make([]string, 205)
	for index := range arns {
		arns[index] = "task-" + strconv.Itoa(index)
	}
	values := func(yield func(string) bool) {
		for _, arn := range arns {
			if !yield(arn) {
				return
			}
		}
	}
	batches := make([][]string, 0, 3)
	for batch := range taskARNBatches(values, 100) {
		batches = append(batches, batch)
	}
	if len(batches) != 3 || len(batches[0]) != 100 || len(batches[1]) != 100 || len(batches[2]) != 5 {
		t.Fatalf("batch sizes = %d/%d/%d, want 100/100/5", len(batches[0]), len(batches[1]), len(batches[2]))
	}
	if len(batches[0])+len(batches[1])+len(batches[2]) != len(arns) {
		t.Fatal("task ARN batching lost values")
	}
}

func TestTaskARNBatchesRejectsInvalidSize(t *testing.T) {
	values := iter.Seq[string](func(yield func(string) bool) { yield("task") })
	for range taskARNBatches(values, 0) {
		t.Fatal("invalid batch size yielded a batch")
	}
}
