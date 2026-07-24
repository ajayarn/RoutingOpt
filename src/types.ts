export interface Customer {
  id: number;
  x: number;
  y: number;
  demand: number;
  readyTime: number;
  dueDate: number;
  serviceTime: number;
}

export interface VRPTWInstance {
  name: string;
  vehicleNumber: number;
  capacity: number;
  depot: Customer;
  customers: Customer[];
}

export interface Route {
  vehicleId: number;
  customerIds: number[];
  distance: number;
  load: number;
  arrivalTimes: { [customerId: number]: number };
  waitingTimes: { [customerId: number]: number };
  departureTimes: { [customerId: number]: number };
}

export interface SolverSolution {
  routes: Route[];
  totalDistance: number;
  totalVehicles: number;
  isFeasible: boolean;
  computationTimeMs: number;
  iteration?: number;
}

export interface SolverParams {
  algorithm: 'ga' | 'lns' | 'sa';
  maxIterations: number;
  populationSize?: number;
  mutationRate?: number;
  destroyRate?: number;
  llmThreshold?: number;
  useLlm?: boolean;
  useLkh?: boolean;
}

export interface SolverProgressMessage {
  type: 'progress' | 'result' | 'error' | 'start';
  iteration?: number;
  bestDistance?: number;
  bestVehicles?: number;
  routes?: Route[];
  computationTimeMs?: number;
  message?: string;
}
