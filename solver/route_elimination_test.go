package main

import "testing"

func testCustomers() map[int]Customer {
	return map[int]Customer{
		0: {ID: 0, X: 0, Y: 0, Demand: 0, ReadyTime: 0, DueDate: 1000, ServiceTime: 0},
		1: {ID: 1, X: 1, Y: 0, Demand: 5, ReadyTime: 0, DueDate: 100, ServiceTime: 0},
		2: {ID: 2, X: 2, Y: 0, Demand: 5, ReadyTime: 0, DueDate: 100, ServiceTime: 0},
		3: {ID: 3, X: 3, Y: 0, Demand: 5, ReadyTime: 0, DueDate: 100, ServiceTime: 0},
		4: {ID: 4, X: 4, Y: 0, Demand: 9, ReadyTime: 0, DueDate: 100, ServiceTime: 0},
	}
}

func testDepot() Customer {
	return Customer{ID: 0, X: 0, Y: 0, Demand: 0, ReadyTime: 0, DueDate: 1000, ServiceTime: 0}
}

func TestMinVehiclesLowerBound(t *testing.T) {
	customers := testCustomers()
	got := minVehiclesLowerBound(customers, 10)
	want := 3 // total demand 5+5+5+9=24, ceil(24/10)=3
	if got != want {
		t.Fatalf("minVehiclesLowerBound() = %d, want %d", got, want)
	}
}
