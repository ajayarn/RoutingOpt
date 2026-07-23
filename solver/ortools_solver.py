#!/usr/bin/env python3
import sys
import json
import argparse
import math
import time
from ortools.constraint_solver import routing_enums_pb2
from ortools.constraint_solver import pywrapcp

# Custom JSON Printer for SSE compatibility
def send_start(message):
    print(json.dumps({"type": "start", "message": message}), flush=True)

def send_progress(iteration, distance, vehicles, routes, elapsed_ms):
    print(json.dumps({
        "type": "progress",
        "iteration": iteration,
        "bestDistance": round(distance, 2),
        "bestVehicles": vehicles,
        "routes": routes,
        "computationTimeMs": elapsed_ms
    }), flush=True)

def send_result(distance, vehicles, routes, elapsed_ms, message):
    print(json.dumps({
        "type": "result",
        "bestDistance": round(distance, 2),
        "bestVehicles": vehicles,
        "routes": routes,
        "computationTimeMs": elapsed_ms,
        "message": message
    }), flush=True)

def send_error(message):
    print(json.dumps({"type": "error", "message": message}), flush=True)

# Parse Solomon benchmark text format
def parse_solomon_file(file_path):
    with open(file_path, 'r') as f:
        lines = f.readlines()
    
    name = ""
    capacity = 0.0
    vehicle_number = 0
    depot = {}
    customers = []
    
    state = 0  # 0: header, 1: vehicle, 2: customer-header, 3: customer-data
    
    for line in lines:
        line = line.strip()
        if not line:
            continue
        
        if not name and state == 0:
            name = line
            continue
            
        if "VEHICLE" in line:
            state = 1
            continue
        if "CUSTOMER" in line:
            state = 2
            continue
            
        fields = line.split()
        if not fields:
            continue
            
        if state == 1:
            if fields[0] == "NUMBER" or fields[0] == "CAPACITY":
                continue
            try:
                vehicle_number = int(fields[0])
                capacity = float(fields[1])
                state = 0
            except ValueError:
                pass
        elif state == 2:
            if "CUST NO." in line or "XCOORD" in line:
                continue
            state = 3
            
        if state == 3:
            if len(fields) < 7:
                continue
            try:
                c_id = int(fields[0])
                x = float(fields[1])
                y = float(fields[2])
                demand = float(fields[3])
                ready = float(fields[4])
                due = float(fields[5])
                service = float(fields[6])
                
                c = {
                    "id": c_id,
                    "x": x,
                    "y": y,
                    "demand": demand,
                    "readyTime": ready,
                    "dueDate": due,
                    "serviceTime": service
                }
                
                if c_id == 0:
                    depot = c
                else:
                    customers.append(c)
            except ValueError:
                continue
                
    return name, vehicle_number, capacity, depot, customers

def distance(c1, c2):
    return math.sqrt((c1["x"] - c2["x"])**2 + (c1["y"] - c2["y"])**2)

# Solve entire VRPTW using Google OR-Tools
def solve_full_vrptw(file_path, max_seconds=10):
    start_time = time.time()
    try:
        name, vehicle_num, capacity, depot, customers = parse_solomon_file(file_path)
    except Exception as e:
        send_error(f"Failed to parse Solomon file: {str(e)}")
        return
        
    all_nodes = [depot] + customers
    num_nodes = len(all_nodes)
    
    # Precompute distance and time matrices
    dist_matrix = []
    for i in range(num_nodes):
        row = []
        for j in range(num_nodes):
            row.append(distance(all_nodes[i], all_nodes[j]))
        dist_matrix.append(row)
        
    # Create Routing Index Manager
    # Use depot index 0
    manager = pywrapcp.RoutingIndexManager(num_nodes, vehicle_num, 0)
    routing = pywrapcp.RoutingModel(manager)
    
    # Distance/Transit Callback
    def distance_callback(from_index, to_index):
        from_node = manager.IndexToNode(from_index)
        to_node = manager.IndexToNode(to_index)
        return int(dist_matrix[from_node][to_node] * 1000)  # scale to avoid float issues
        
    transit_callback_index = routing.RegisterTransitCallback(distance_callback)
    
    # Cost Evaluator
    routing.SetArcCostEvaluatorOfAllVehicles(transit_callback_index)
    
    # Add Distance Dimension (to compute total distance)
    routing.AddDimension(
        transit_callback_index,
        0,  # no slack
        3000000,  # max distance scaled
        True,  # start cumul to zero
        "Distance"
    )
    
    # Add Capacity Dimension
    def demand_callback(from_index):
        from_node = manager.IndexToNode(from_index)
        return int(all_nodes[from_node]["demand"])
        
    demand_callback_index = routing.RegisterUnaryTransitCallback(demand_callback)
    routing.AddDimensionWithVehicleCapacity(
        demand_callback_index,
        0,  # null capacity slack
        [int(capacity)] * vehicle_num,  # capacities
        True,  # start cumul to zero
        "Capacity"
    )
    
    # Add Time Dimension (Time Windows)
    def time_callback(from_index, to_index):
        from_node = manager.IndexToNode(from_index)
        to_node = manager.IndexToNode(to_index)
        # transit time + service time
        transit = dist_matrix[from_node][to_node]
        service = all_nodes[from_node]["serviceTime"]
        return int((transit + service) * 1000)
        
    time_callback_index = routing.RegisterTransitCallback(time_callback)
    routing.AddDimension(
        time_callback_index,
        5000000,  # allow large waiting time slack
        5000000,  # max time per vehicle
        False,  # don't force start to zero (can start late, but Solomon usually starts at 0)
        "Time"
    )
    
    time_dimension = routing.GetDimensionOrDie("Time")
    # Add time windows for each node
    for node_idx, node in enumerate(all_nodes):
        index = manager.NodeToIndex(node_idx)
        time_dimension.CumulVar(index).SetRange(
            int(node["readyTime"] * 1000),
            int(node["dueDate"] * 1000)
        )
        
    # To minimize the number of vehicles, we set a high fixed cost for each active vehicle
    # VRPTW benchmarks prioritize minimizing vehicle count, then distance
    VEHICLE_PENALTY = 1000000  # huge penalty scaled
    for vehicle_id in range(vehicle_num):
        routing.SetFixedCostOfVehicle(VEHICLE_PENALTY, vehicle_id)
        
    # Search Parameters
    search_parameters = pywrapcp.DefaultRoutingSearchParameters()
    search_parameters.first_solution_strategy = (
        routing_enums_pb2.FirstSolutionStrategy.PATH_CHEAPEST_ARC
    )
    search_parameters.local_search_metaheuristic = (
        routing_enums_pb2.LocalSearchMetaheuristic.GUIDED_LOCAL_SEARCH
    )
    search_parameters.time_limit.seconds = max_seconds
    
    # Custom step callbacks can be used, but since we run very fast, we can solve and stream
    send_start(f"Running Google OR-Tools Routing Solver on {name} (Time Limit: {max_seconds}s)")
    
    solution = routing.SolveWithParameters(search_parameters)
    elapsed_ms = int((time.time() - start_time) * 1000)
    
    if solution:
        routes_out = []
        total_dist = 0.0
        active_vehicles = 0
        
        for vehicle_id in range(vehicle_num):
            index = routing.Start(vehicle_id)
            if routing.IsEnd(index):
                continue
                
            cust_ids = []
            arrival_times = {}
            waiting_times = {}
            departure_times = {}
            
            route_dist = 0.0
            route_load = 0.0
            
            # Start node details
            curr_node = manager.IndexToNode(index)
            curr_time = solution.Value(time_dimension.CumulVar(index)) / 1000.0
            
            prev_node_idx = curr_node
            index = solution.Value(routing.NextVar(index))
            
            while not routing.IsEnd(index):
                node_idx = manager.IndexToNode(index)
                cust = all_nodes[node_idx]
                cust_ids.append(cust["id"])
                
                route_load += cust["demand"]
                
                # Retrieve time window cumuls
                arr_val = solution.Value(time_dimension.CumulVar(index)) / 1000.0
                arrival_times[cust["id"]] = arr_val
                
                # Check actual arrival and wait
                travel_time = dist_matrix[prev_node_idx][node_idx]
                prev_cust = all_nodes[prev_node_idx]
                prev_departure = departure_times.get(prev_cust["id"], 0.0) if prev_cust["id"] != 0 else 0.0
                
                real_arrival = prev_departure + travel_time
                wait_time = max(0.0, cust["readyTime"] - real_arrival)
                waiting_times[cust["id"]] = wait_time
                departure_times[cust["id"]] = real_arrival + wait_time + cust["serviceTime"]
                
                prev_node_idx = node_idx
                index = solution.Value(routing.NextVar(index))
                
            if len(cust_ids) > 0:
                active_vehicles += 1
                # Calculate final route distance properly
                # depot -> cust1 -> ... -> depot
                r_dist = 0.0
                curr = depot
                for cid in cust_ids:
                    # Find customer
                    c = next(x for x in customers if x["id"] == cid)
                    r_dist += distance(curr, c)
                    curr = c
                r_dist += distance(curr, depot)
                total_dist += r_dist
                
                routes_out.append({
                    "vehicleId": len(routes_out) + 1,
                    "customerIds": cust_ids,
                    "distance": round(r_dist, 2),
                    "load": route_load,
                    "arrivalTimes": {str(k): round(v, 2) for k, v in arrival_times.items()},
                    "waitingTimes": {str(k): round(v, 2) for k, v in waiting_times.items()},
                    "departureTimes": {str(k): round(v, 2) for k, v in departure_times.items()}
                })
                
        send_result(total_dist, active_vehicles, routes_out, elapsed_ms, "Optimized successfully using Google OR-Tools.")
    else:
        send_error("No feasible solution found with OR-Tools.")

# Solve TSP with Time Windows (TSPTW) subproblem for each route
def optimize_subproblem_routes(file_path, routes_json_str):
    start_time = time.time()
    try:
        name, _, capacity, depot, customers = parse_solomon_file(file_path)
    except Exception as e:
        print(json.dumps({"error": f"Failed to parse Solomon file: {str(e)}"}), flush=True)
        return
        
    customer_map = {c["id"]: c for c in customers}
    customer_map[0] = depot
    
    try:
        input_routes = json.loads(routes_json_str)
    except Exception as e:
        print(json.dumps({"error": f"Failed to parse input routes: {str(e)}"}), flush=True)
        return
        
    optimized_routes = []
    
    # We will optimize each route individually as a TSPTW subproblem using OR-Tools
    for r in input_routes:
        c_ids = r.get("customerIds", [])
        if len(c_ids) <= 2:
            # Too short to optimize, keep as is
            optimized_routes.append(r)
            continue
            
        # Extract subset of customers for this route
        route_nodes = [depot] + [customer_map[cid] for cid in c_ids]
        n_nodes = len(route_nodes)
        
        # Build local distance/time matrices
        local_dist = []
        for i in range(n_nodes):
            row = []
            for j in range(n_nodes):
                row.append(distance(route_nodes[i], route_nodes[j]))
            local_dist.append(row)
            
        # Run local OR-Tools TSPTW solver
        manager = pywrapcp.RoutingIndexManager(n_nodes, 1, 0)
        routing = pywrapcp.RoutingModel(manager)
        
        def local_distance_callback(from_index, to_index):
            from_node = manager.IndexToNode(from_index)
            to_node = manager.IndexToNode(to_index)
            return int(local_dist[from_node][to_node] * 1000)
            
        transit_idx = routing.RegisterTransitCallback(local_distance_callback)
        routing.SetArcCostEvaluatorOfAllVehicles(transit_idx)
        
        # Distance dimension
        routing.AddDimension(transit_idx, 0, 3000000, True, "Distance")
        
        # Time dimension
        def local_time_callback(from_index, to_index):
            from_node = manager.IndexToNode(from_index)
            to_node = manager.IndexToNode(to_index)
            transit = local_dist[from_node][to_node]
            service = route_nodes[from_node]["serviceTime"]
            return int((transit + service) * 1000)
            
        time_transit_idx = routing.RegisterTransitCallback(local_time_callback)
        routing.AddDimension(time_transit_idx, 5000000, 5000000, False, "Time")
        time_dimension = routing.GetDimensionOrDie("Time")
        
        # Set ready/due times
        for node_idx, node in enumerate(route_nodes):
            index = manager.NodeToIndex(node_idx)
            time_dimension.CumulVar(index).SetRange(
                int(node["readyTime"] * 1000),
                int(node["dueDate"] * 1000)
            )
            
        # Search parameters for TSPTW (very fast)
        search_params = pywrapcp.DefaultRoutingSearchParameters()
        search_params.first_solution_strategy = (
            routing_enums_pb2.FirstSolutionStrategy.PATH_CHEAPEST_ARC
        )
        search_params.time_limit.seconds = 1  # 1 second max for subproblem
        
        solution = routing.SolveWithParameters(search_params)
        
        if solution:
            index = routing.Start(0)
            new_cust_ids = []
            arrival_times = {}
            waiting_times = {}
            departure_times = {}
            
            route_load = 0.0
            prev_node_idx = 0
            
            index = solution.Value(routing.NextVar(index))
            while not routing.IsEnd(index):
                node_idx = manager.IndexToNode(index)
                cust = route_nodes[node_idx]
                new_cust_ids.append(cust["id"])
                route_load += cust["demand"]
                
                arr_val = solution.Value(time_dimension.CumulVar(index)) / 1000.0
                arrival_times[cust["id"]] = arr_val
                
                # Check actual arrival and wait
                travel_time = local_dist[prev_node_idx][node_idx]
                prev_cust = route_nodes[prev_node_idx]
                prev_departure = departure_times.get(prev_cust["id"], 0.0) if prev_cust["id"] != 0 else 0.0
                
                real_arrival = prev_departure + travel_time
                wait_time = max(0.0, cust["readyTime"] - real_arrival)
                waiting_times[cust["id"]] = wait_time
                departure_times[cust["id"]] = real_arrival + wait_time + cust["serviceTime"]
                
                prev_node_idx = node_idx
                index = solution.Value(routing.NextVar(index))
                
            # Recompute total distance of optimized route
            r_dist = 0.0
            curr = depot
            for cid in new_cust_ids:
                c = customer_map[cid]
                r_dist += distance(curr, c)
                curr = c
            r_dist += distance(curr, depot)
            
            optimized_routes.append({
                "vehicleId": r.get("vehicleId"),
                "customerIds": new_cust_ids,
                "distance": round(r_dist, 2),
                "load": route_load,
                "arrivalTimes": {str(k): round(v, 2) for k, v in arrival_times.items()},
                "waitingTimes": {str(k): round(v, 2) for k, v in waiting_times.items()},
                "departureTimes": {str(k): round(v, 2) for k, v in departure_times.items()}
            })
        else:
            # Fallback to original route if no feasible re-sequence found
            optimized_routes.append(r)
            
    # Output optimized routes back as JSON
    print(json.dumps(optimized_routes))

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Google OR-Tools VRPTW Solver / Subproblem Optimizer")
    parser.add_argument("-file", type=str, required=True, help="Solomon benchmark file path")
    parser.add_argument("-mode", type=str, default="full", choices=["full", "subproblem"], help="Solver mode")
    parser.add_argument("-routes", type=str, default="", help="JSON string of routes for subproblem optimization")
    parser.add_argument("-seconds", type=int, default=10, help="Max time limit in seconds")
    
    args = parser.parse_args()
    
    if args.mode == "full":
        solve_full_vrptw(args.file, args.seconds)
    elif args.mode == "subproblem":
        optimize_subproblem_routes(args.file, args.routes)
