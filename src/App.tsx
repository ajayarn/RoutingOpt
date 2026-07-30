import { useState, useEffect, useRef } from 'react';
import { 
  Play, 
  RotateCcw, 
  Upload, 
  Truck, 
  MapPin, 
  Clock, 
  Layers,
  AlertCircle,
  TrendingDown,
  Info,
  Sliders,
  FileText
} from 'lucide-react';
import { Customer, VRPTWInstance, Route, SolverSolution, SolverParams, SolverProgressMessage } from './types';
import InstanceCombobox from './InstanceCombobox';
import { parseSolomonText } from './parseSolomon';

// Standard 6 distinct high-contrast colors for routes
const ROUTE_COLORS = [
  '#2563eb', // blue
  '#16a34a', // green
  '#dc2626', // red
  '#ca8a04', // yellow/amber
  '#7c3aed', // purple
  '#0891b2', // cyan
  '#ea580c', // orange
  '#db2777', // pink
  '#4b5563', // gray
  '#059669', // emerald
  '#4f46e5', // indigo
  '#b45309'  // brown
];

// Source: https://www.sintef.no/projectweb/top/vrptw/100-customers/ (all 56 Solomon
// 100-customer instances). Note this replaced a handful of previously-hardcoded values
// that turned out to be wrong (r101 was 1645.79, rc201 was 1261.67 - the latter was
// actually rc103's value) - trust this table over old numbers seen elsewhere.
export const BEST_KNOWN_SOLUTIONS: Record<string, { distance: number; vehicles: number }> = {
  // C1 series
  c101: { distance: 828.94, vehicles: 10 },
  c102: { distance: 828.94, vehicles: 10 },
  c103: { distance: 828.06, vehicles: 10 },
  c104: { distance: 824.78, vehicles: 10 },
  c105: { distance: 828.94, vehicles: 10 },
  c106: { distance: 828.94, vehicles: 10 },
  c107: { distance: 828.94, vehicles: 10 },
  c108: { distance: 828.94, vehicles: 10 },
  c109: { distance: 828.94, vehicles: 10 },
  // C2 series
  c201: { distance: 591.56, vehicles: 3 },
  c202: { distance: 591.56, vehicles: 3 },
  c203: { distance: 591.17, vehicles: 3 },
  c204: { distance: 590.6, vehicles: 3 },
  c205: { distance: 588.88, vehicles: 3 },
  c206: { distance: 588.49, vehicles: 3 },
  c207: { distance: 588.29, vehicles: 3 },
  c208: { distance: 588.32, vehicles: 3 },
  // R1 series
  r101: { distance: 1650.8, vehicles: 19 },
  r102: { distance: 1486.12, vehicles: 17 },
  r103: { distance: 1292.68, vehicles: 13 },
  r104: { distance: 1007.31, vehicles: 9 },
  r105: { distance: 1377.11, vehicles: 14 },
  r106: { distance: 1252.03, vehicles: 12 },
  r107: { distance: 1104.66, vehicles: 10 },
  r108: { distance: 960.88, vehicles: 9 },
  r109: { distance: 1194.73, vehicles: 11 },
  r110: { distance: 1118.84, vehicles: 10 },
  r111: { distance: 1096.73, vehicles: 10 },
  r112: { distance: 982.14, vehicles: 9 },
  // R2 series
  r201: { distance: 1252.37, vehicles: 4 },
  r202: { distance: 1191.7, vehicles: 3 },
  r203: { distance: 939.5, vehicles: 3 },
  r204: { distance: 825.52, vehicles: 2 },
  r205: { distance: 994.43, vehicles: 3 },
  r206: { distance: 906.14, vehicles: 3 },
  r207: { distance: 890.61, vehicles: 2 },
  r208: { distance: 726.82, vehicles: 2 },
  r209: { distance: 909.16, vehicles: 3 },
  r210: { distance: 939.37, vehicles: 3 },
  r211: { distance: 885.71, vehicles: 2 },
  // RC1 series
  rc101: { distance: 1696.95, vehicles: 14 },
  rc102: { distance: 1554.75, vehicles: 12 },
  rc103: { distance: 1261.67, vehicles: 11 },
  rc104: { distance: 1135.48, vehicles: 10 },
  rc105: { distance: 1629.44, vehicles: 13 },
  rc106: { distance: 1424.73, vehicles: 11 },
  rc107: { distance: 1230.48, vehicles: 11 },
  rc108: { distance: 1139.82, vehicles: 10 },
  // RC2 series
  rc201: { distance: 1406.94, vehicles: 4 },
  rc202: { distance: 1365.65, vehicles: 3 },
  rc203: { distance: 1049.62, vehicles: 3 },
  rc204: { distance: 798.46, vehicles: 3 },
  rc205: { distance: 1297.65, vehicles: 4 },
  rc206: { distance: 1146.32, vehicles: 3 },
  rc207: { distance: 1061.14, vehicles: 3 },
  rc208: { distance: 828.14, vehicles: 3 },
};

export const OPTIMAL_SOLUTIONS: Record<string, { distance: number; vehicles: number; routes: number[][] }> = {
  c101: {
    distance: 828.94,
    vehicles: 10,
    routes: [
      [81, 78, 76, 71, 70, 73, 77, 79, 80],
      [57, 55, 54, 53, 56, 58, 60, 59],
      [98, 96, 95, 94, 92, 93, 97, 100, 99],
      [32, 33, 31, 35, 37, 38, 39, 36, 34],
      [13, 17, 18, 19, 15, 16, 14, 12],
      [90, 87, 86, 83, 82, 84, 85, 88, 89, 91],
      [43, 42, 41, 40, 44, 46, 45, 48, 51, 50, 52, 49, 47],
      [67, 65, 63, 62, 74, 72, 61, 64, 68, 66, 69],
      [5, 3, 7, 8, 10, 11, 9, 6, 4, 2, 1, 75],
      [20, 24, 25, 27, 29, 30, 28, 26, 23, 22, 21]
    ]
  }
};

// SINTEF rounds published best-known distances to 2 decimals, so the solver's full-precision
// totalDistance must be rounded the same way before comparing - otherwise float noise below the
// published value reads as a false tie/improvement. Vehicle count (the primary objective) is
// compared first and dominates the verdict regardless of distance.
function computeGapToOptimal(solution: SolverSolution, bks: { distance: number; vehicles: number }) {
  if (solution.totalVehicles < bks.vehicles) {
    return { text: `New best — fewer vehicles (${solution.totalVehicles} vs ${bks.vehicles})`, tone: 'good' as const };
  }
  if (solution.totalVehicles > bks.vehicles) {
    return { text: `+${solution.totalVehicles - bks.vehicles} vehicle(s) vs best known`, tone: 'bad' as const };
  }

  const roundedDistance = Math.round(solution.totalDistance * 100) / 100;
  const gapPct = ((roundedDistance - bks.distance) / bks.distance) * 100;
  if (gapPct < 0) {
    return { text: `${gapPct.toFixed(2)}% (beats best known!)`, tone: 'good' as const };
  }
  if (gapPct === 0) {
    return { text: '0.00% (Optimal)', tone: 'good' as const };
  }
  return { text: `+${gapPct.toFixed(2)}%`, tone: 'bad' as const };
}

function formatExportTimestamp(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${pad(date.getDate())}.${pad(date.getMonth() + 1)}.${date.getFullYear()} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

// SINTEF detailed-solution file format (see e.g. the published c101 solution) - depot (customer
// 0) is implicit and omitted from each route's customer list, matching Route.customerIds.
function exportSolutionAsText(solution: SolverSolution, instanceId: string, instanceName: string) {
  const lines = [
    `Instance name : ${instanceName}`,
    `Authors       : Ajay Arn (RoutingOpt)`,
    `Date          : ${formatExportTimestamp(new Date())}`,
    `Reference     : https://github.com/ajayarn/RoutingOpt`,
    `Solution`,
    ...solution.routes.map((route, idx) => `Route  ${idx + 1} : ${route.customerIds.join(' ')}`),
  ];

  const blob = new Blob([lines.join('\n') + '\n'], { type: 'text/plain' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `${instanceId}_solution.txt`;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

export default function App() {
  // Solver parameters
  const [params, setParams] = useState<SolverParams>({
    maxIterations: 1000,
    llmThreshold: 20,
    useLkh: false
  });

  // Data states
  const [instances, setInstances] = useState<Array<{ id: string; name: string; customersCount: number; capacity: number; vehicles: number }>>([]);
  const [selectedInstanceId, setSelectedInstanceId] = useState<string>('c101');
  const [instanceData, setInstanceData] = useState<VRPTWInstance | null>(null);
  
  // Solver states
  const [isSolving, setIsSolving] = useState(false);
  const [solution, setSolution] = useState<SolverSolution | null>(null);
  const [progressHistory, setProgressHistory] = useState<Array<{ iteration: number; distance: number; vehicles: number }>>([]);
  const [activeMessage, setActiveMessage] = useState<string>('');
  const [solverLogs, setSolverLogs] = useState<string[]>([]);
  const [logFilter, setLogFilter] = useState<'all' | 'improvements' | 'llm' | 'lns'>('all');
  const [elapsedTime, setElapsedTime] = useState(0);

  // UI Selection states
  const [selectedRouteId, setSelectedRouteId] = useState<number | null>(null);
  const [hoveredCustomer, setHoveredCustomer] = useState<Customer | null>(null);
  const [hoveredRoute, setHoveredRoute] = useState<Route | null>(null);
  const [showUploadModal, setShowUploadModal] = useState(false);
  const [uploadName, setUploadName] = useState('');
  const [uploadContent, setUploadContent] = useState('');
  const [uploadError, setUploadError] = useState('');
  const [isViewingOptimal, setIsViewingOptimal] = useState(false);
  // Raw text of instances uploaded this session - there's no backend to
  // persist these to (a static host has nowhere to write a file), so they
  // only live in memory and are gone on refresh.
  const [uploadedInstanceText, setUploadedInstanceText] = useState<Record<string, string>>({});

  const workerRef = useRef<Worker | null>(null);
  const timerRef = useRef<NodeJS.Timeout | null>(null);
  const logScrollRef = useRef<HTMLDivElement | null>(null);

  // Load instances list on mount
  useEffect(() => {
    fetchInstances();
  }, []);

  // Fetch current instance data when selectedInstanceId changes
  useEffect(() => {
    if (selectedInstanceId) {
      fetchInstanceData(selectedInstanceId);
      // Reset solution
      setSolution(null);
      setProgressHistory([]);
      setSelectedRouteId(null);
      setHoveredRoute(null);
      setActiveMessage('');
      setIsViewingOptimal(false);
    }
  }, [selectedInstanceId]);

  // Handle timer during solving
  useEffect(() => {
    if (isSolving) {
      const start = Date.now();
      timerRef.current = setInterval(() => {
        setElapsedTime(Math.floor((Date.now() - start) / 1000));
      }, 1000);
    } else {
      if (timerRef.current) {
        clearInterval(timerRef.current);
      }
    }
    return () => {
      if (timerRef.current) clearInterval(timerRef.current);
    };
  }, [isSolving]);

  // Auto-scroll logs to bottom when updated
  useEffect(() => {
    if (logScrollRef.current) {
      logScrollRef.current.scrollTop = logScrollRef.current.scrollHeight;
    }
  }, [solverLogs, logFilter]);

  // Terminate any in-flight solve worker on unmount, so a mid-solve
  // navigation away doesn't leave the WASM solver burning CPU in the
  // background or postMessage-ing into an unmounted component.
  useEffect(() => {
    return () => {
      workerRef.current?.terminate();
    };
  }, []);

  // No backend to ask for the instance list or a parsed instance (this app
  // is a fully static site - GitHub Pages, no Express) - fetch each raw
  // Solomon file directly and parse it client-side via parseSolomon.ts.
  // Paths are relative (not "/data/...") so they resolve correctly whether
  // the app is served from the domain root or a GitHub Pages subpath.
  const fetchInstances = async () => {
    try {
      const ids = Object.keys(BEST_KNOWN_SOLUTIONS);
      const results = await Promise.all(ids.map(async (id) => {
        try {
          const res = await fetch(`data/${id}.txt`);
          if (!res.ok) return null;
          const parsed = parseSolomonText(await res.text());
          return {
            id,
            name: parsed.name || id.toUpperCase(),
            customersCount: parsed.customers.length,
            capacity: parsed.capacity,
            vehicles: parsed.vehicleNumber
          };
        } catch {
          return null;
        }
      }));
      const data = results.filter((r): r is NonNullable<typeof r> => r !== null);
      setInstances(data);
      if (data.length > 0 && !selectedInstanceId) {
        setSelectedInstanceId(data[0].id);
      }
    } catch (e) {
      console.error('Failed to load instances', e);
    }
  };

  const fetchInstanceData = async (id: string) => {
    try {
      const uploadedText = uploadedInstanceText[id];
      const text = uploadedText !== undefined ? uploadedText : await (await fetch(`data/${id}.txt`)).text();
      setInstanceData(parseSolomonText(text));
    } catch (e) {
      console.error('Failed to load instance data', e);
    }
  };

  const handleUpload = () => {
    if (!uploadName || !uploadContent) {
      setUploadError('Please provide both a name and file contents.');
      return;
    }
    try {
      const safeName = uploadName.replace(/[^a-zA-Z0-9_-]/g, '').toLowerCase();
      const parsed = parseSolomonText(uploadContent);
      // Held in memory only - a static deploy has nowhere to persist a
      // file, so this (like everything else here) has to work without a
      // backend; it's gone on refresh.
      setUploadedInstanceText(prev => ({ ...prev, [safeName]: uploadContent }));
      setInstances(prev => [
        ...prev.filter(i => i.id !== safeName),
        {
          id: safeName,
          name: parsed.name || safeName.toUpperCase(),
          customersCount: parsed.customers.length,
          capacity: parsed.capacity,
          vehicles: parsed.vehicleNumber
        }
      ]);
      setShowUploadModal(false);
      setUploadName('');
      setUploadContent('');
      setUploadError('');
      setSelectedInstanceId(safeName);
    } catch (e) {
      setUploadError('Failed to process the uploaded content.');
    }
  };

  const startSolver = async () => {
    if (isSolving) return;

    setIsSolving(true);
    setSolution(null);
    setProgressHistory([]);
    setSolverLogs([]);
    setSelectedRouteId(null);
    setHoveredRoute(null);
    setElapsedTime(0);
    setIsViewingOptimal(false);
    setActiveMessage('Loading instance data...');

    let instanceText: string;
    try {
      const uploadedText = uploadedInstanceText[selectedInstanceId];
      if (uploadedText !== undefined) {
        instanceText = uploadedText;
      } else {
        const res = await fetch(`data/${selectedInstanceId}.txt`);
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        instanceText = await res.text();
      }
    } catch (e) {
      setActiveMessage(`Error: failed to load instance file (${e instanceof Error ? e.message : e})`);
      setIsSolving(false);
      return;
    }

    setActiveMessage('Initializing Go VRPTW Solver (WASM)...');

    // Relative path - resolves correctly whether served from the domain
    // root or a GitHub Pages project subpath.
    const worker = new Worker('solverWorker.js');
    workerRef.current = worker;

    worker.onmessage = (event) => {
      const msg: SolverProgressMessage = event.data;
      if (msg.type === 'start') {
        const startMsg = msg.message || 'Solver started...';
        setActiveMessage(startMsg);
        setSolverLogs([startMsg]);
      } else if (msg.type === 'progress') {
        if (msg.routes && msg.bestDistance !== undefined && msg.bestVehicles !== undefined) {
          const currentSol: SolverSolution = {
            routes: msg.routes,
            totalDistance: msg.bestDistance,
            totalVehicles: msg.bestVehicles,
            computationTimeMs: msg.computationTimeMs || 0,
            iteration: msg.iteration
          };
          setSolution(currentSol);
          setProgressHistory(prev => [
            ...prev,
            {
              iteration: msg.iteration || 0,
              distance: msg.bestDistance || 0,
              vehicles: msg.bestVehicles || 0
            }
          ]);

          if (msg.message) {
            setActiveMessage(msg.message);
            setSolverLogs(prev => [...prev, msg.message]);
          } else {
            setActiveMessage(`Optimizing... Iteration ${msg.iteration}/${params.maxIterations}`);
          }
        }
      } else if (msg.type === 'result') {
        if (msg.routes && msg.bestDistance !== undefined && msg.bestVehicles !== undefined) {
          const finalSol: SolverSolution = {
            routes: msg.routes,
            totalDistance: msg.bestDistance,
            totalVehicles: msg.bestVehicles,
            computationTimeMs: msg.computationTimeMs || 0,
            iteration: msg.iteration ?? params.maxIterations
          };
          setSolution(finalSol);
          const resultMsg = msg.message || 'Optimization completed successfully.';
          setActiveMessage(resultMsg);
          setSolverLogs(prev => [...prev, resultMsg]);
        }
        worker.terminate();
        workerRef.current = null;
        setIsSolving(false);
      } else if (msg.type === 'error') {
        const errMsg = `Error: ${msg.message}`;
        setActiveMessage(errMsg);
        setSolverLogs(prev => [...prev, errMsg]);
        worker.terminate();
        workerRef.current = null;
        setIsSolving(false);
      }
    };

    worker.onerror = (e) => {
      console.error('Worker error', e);
      setActiveMessage(`Error: solver worker crashed (${e.message || 'unknown error'})`);
      worker.terminate();
      workerRef.current = null;
      setIsSolving(false);
    };

    worker.postMessage({
      type: 'solve',
      instanceText,
      args: {
        iterations: params.maxIterations,
        llmThreshold: params.llmThreshold ?? 20,
        useLkh: !!params.useLkh,
        seed: Date.now(),
      }
    });
  };

  const stopSolver = () => {
    // The wasm solve loop runs synchronously inside the worker's single JS
    // thread once started - it can't poll for a "please stop" message
    // mid-computation, so terminate() (matching the old child.kill() on the
    // native subprocess) is the only way to interrupt it.
    if (workerRef.current) {
      workerRef.current.terminate();
      workerRef.current = null;
    }
    setIsSolving(false);
    setActiveMessage('Solver interrupted by user.');
  };

  const evaluateRoute = (customerIds: number[], instance: VRPTWInstance): Route => {
    const depot = instance.depot;
    const getCust = (id: number) => id === 0 ? depot : instance.customers.find(c => c.id === id);

    let distAcc = 0;
    let loadAcc = 0;
    let currentTime = 0;
    
    const arrivalTimes: Record<string, number> = {};
    const waitingTimes: Record<string, number> = {};
    const departureTimes: Record<string, number> = {};

    let prevNode = depot;
    
    for (const id of customerIds) {
      const cust = getCust(id);
      if (!cust) continue;

      const d = Math.sqrt((prevNode.x - cust.x) ** 2 + (prevNode.y - cust.y) ** 2);
      distAcc += d;
      loadAcc += cust.demand;

      const arrTime = currentTime + d;
      arrivalTimes[id] = arrTime;
      
      const waitTime = Math.max(0, cust.readyTime - arrTime);
      waitingTimes[id] = waitTime;
      
      currentTime = arrTime + waitTime + cust.serviceTime;
      departureTimes[id] = currentTime;

      prevNode = cust;
    }

    // Back to depot
    const dToDepot = Math.sqrt((prevNode.x - depot.x) ** 2 + (prevNode.y - depot.y) ** 2);
    distAcc += dToDepot;

    return {
      vehicleId: 0,
      customerIds,
      distance: Math.round(distAcc * 100) / 100,
      load: loadAcc,
      arrivalTimes: Object.fromEntries(Object.entries(arrivalTimes).map(([k, v]) => [k, Math.round(v * 100) / 100])),
      waitingTimes: Object.fromEntries(Object.entries(waitingTimes).map(([k, v]) => [k, Math.round(v * 100) / 100])),
      departureTimes: Object.fromEntries(Object.entries(departureTimes).map(([k, v]) => [k, Math.round(v * 100) / 100])),
    };
  };

  const loadOptimalReference = () => {
    if (!instanceData) return;
    
    const opt = OPTIMAL_SOLUTIONS[selectedInstanceId];
    if (!opt) return;

    const routes: Route[] = opt.routes.map((customerIds, idx) => {
      const r = evaluateRoute(customerIds, instanceData);
      r.vehicleId = idx + 1;
      return r;
    });

    const totalDistance = routes.reduce((sum, r) => sum + r.distance, 0);
    const totalVehicles = routes.length;

    const sol: SolverSolution = {
      routes,
      totalDistance,
      totalVehicles,
      computationTimeMs: 0,
      iteration: 0
    };

    setSolution(sol);
    setIsViewingOptimal(true);
    setSelectedRouteId(null);
    setProgressHistory([]);
    setActiveMessage('Loaded known optimal reference solution (828.94 / 10 vehicles).');
  };

  // Helper mapping customer ID to customer details
  const getCustomer = (id: number): Customer | null => {
    if (!instanceData) return null;
    if (id === 0) return instanceData.depot;
    return instanceData.customers.find(c => c.id === id) || null;
  };

  // Helper calculating bounding box for SVG fitting
  const getSvgBounds = () => {
    if (!instanceData) return { minX: 0, maxX: 100, minY: 0, maxY: 100, width: 100, height: 100 };
    const coords = [instanceData.depot, ...instanceData.customers];
    const xs = coords.map(c => c.x);
    const ys = coords.map(c => c.y);
    const minX = Math.min(...xs);
    const maxX = Math.max(...xs);
    const minY = Math.min(...ys);
    const maxY = Math.max(...ys);
    const width = maxX - minX;
    const height = maxY - minY;
    
    // Add 10% padding
    const paddingX = width * 0.1 || 10;
    const paddingY = height * 0.1 || 10;
    return {
      minX: minX - paddingX,
      maxX: maxX + paddingX,
      minY: minY - paddingY,
      maxY: maxY + paddingY,
      width: width + paddingX * 2,
      height: height + paddingY * 2
    };
  };

  const bounds = getSvgBounds();

  // Selected route detailed timeline calculations
  const selectedRoute = solution?.routes.find(r => r.vehicleId === selectedRouteId);

  return (
    <div className="min-h-screen bg-slate-50 text-slate-800 flex flex-col font-sans">
      {/* Upper Navigation Header */}
      <header className="bg-white border-b border-slate-200 sticky top-0 z-10 px-6 py-4 flex items-center justify-between">
        <div className="flex items-center space-x-3">
          <div className="p-2 bg-blue-600 rounded-lg text-white">
            <Truck className="h-6 w-6" id="header-truck-icon" />
          </div>
          <div>
            <h1 className="text-xl font-bold tracking-tight text-slate-900" id="header-title">VRPTW Optimization Engine</h1>
            <p className="text-xs text-slate-500 font-normal">Vehicle Routing Problem with Time Windows Solver utilizing LNS metaheuristics with an optional LKH3 sub-solver</p>
          </div>
        </div>
        <div className="flex items-center space-x-3">
          <button 
            id="btn-upload-instance"
            onClick={() => setShowUploadModal(true)}
            className="flex items-center space-x-2 px-3.5 py-2 text-sm font-medium text-slate-700 bg-white border border-slate-300 rounded-lg hover:bg-slate-50 transition-colors"
          >
            <Upload className="h-4 w-4" />
            <span>Upload Solomon File</span>
          </button>
        </div>
      </header>

      {/* Main Grid Workspace */}
      <div className="flex-1 p-6 grid grid-cols-1 lg:grid-cols-12 gap-6 overflow-hidden max-w-[1600px] mx-auto w-full">
        {/* LEFT COLUMN: Controls & Metadata Panel */}
        <div className="lg:col-span-3 flex flex-col gap-6">
          {/* Section 1: Solver Configuration */}
          <div className="bg-white border border-slate-200 rounded-xl shadow-sm p-5 flex flex-col" id="panel-solver-config">
            <div className="flex items-center space-x-2 border-b border-slate-100 pb-3 mb-4">
              <Sliders className="h-4 w-4 text-blue-600" />
              <h2 className="text-sm font-bold uppercase tracking-wider text-slate-500">Solver Controls</h2>
            </div>

            {/* Instance Selector */}
            <div className="mb-4">
              <label className="block text-xs font-semibold text-slate-600 mb-1.5" htmlFor="instance-select">Select Benchmark Instance</label>
              <InstanceCombobox
                instances={instances}
                value={selectedInstanceId}
                disabled={isSolving}
                onChange={setSelectedInstanceId}
              />
            </div>

            {/* Iterations input */}
            <div className="mb-4">
              <label className="block text-xs font-semibold text-slate-600 mb-1.5" htmlFor="iterations-input">Maximum Iterations</label>
              <input
                id="iterations-input"
                type="number"
                min="10"
                max="50000"
                value={params.maxIterations}
                disabled={isSolving}
                onChange={(e) => setParams(prev => ({ ...prev, maxIterations: parseInt(e.target.value) || 100 }))}
                className="w-full text-sm border border-slate-300 rounded-lg p-2 bg-white focus:border-blue-500 focus:ring-1 focus:ring-blue-500 disabled:opacity-50"
              />
            </div>

            {/* Stagnation Threshold */}
            <div className="mb-6">
              <div className="flex justify-between items-center mb-1.5">
                <label className="block text-xs font-semibold text-slate-600" htmlFor="llm-threshold-input">
                  Stagnation Threshold
                </label>
                <span className="text-xs text-blue-600 font-bold font-mono">
                  {params.llmThreshold === 0 ? 'Disabled' : `Iter ${params.llmThreshold}`}
                </span>
              </div>
              <input
                id="llm-threshold-input"
                type="number"
                min="0"
                max={params.maxIterations}
                value={params.llmThreshold ?? 20}
                disabled={isSolving}
                onChange={(e) => setParams(prev => ({ ...prev, llmThreshold: Math.max(0, parseInt(e.target.value) || 0) }))}
                className="w-full text-sm border border-slate-300 rounded-lg p-2 bg-white focus:border-blue-500 focus:ring-1 focus:ring-blue-500 disabled:opacity-50"
              />
              <p className="text-[11px] text-slate-400 mt-1.5 leading-normal">
                After this many stagnant iterations with no improvement, the solver destroys 2-5 routes and re-solves using its built-in heuristic. Set to 0 to bypass.
              </p>

              {/* LKH3 stagnation sub-solver switch */}
              <div className="flex items-center justify-between mt-3 pt-3 border-t border-slate-100">
                <label className="text-xs font-semibold text-slate-600" htmlFor="use-lkh-toggle">
                  Use LKH3 for stagnation sub-solving
                </label>
                <button
                  id="use-lkh-toggle"
                  type="button"
                  role="switch"
                  aria-checked={!!params.useLkh}
                  disabled={isSolving}
                  onClick={() => setParams(prev => ({ ...prev, useLkh: !prev.useLkh }))}
                  className={`shrink-0 relative inline-flex h-5 w-9 items-center rounded-full transition-colors disabled:opacity-50 ${params.useLkh ? 'bg-blue-600' : 'bg-slate-300'}`}
                >
                  <span
                    className={`inline-block h-3.5 w-3.5 transform rounded-full bg-white transition-transform ${params.useLkh ? 'translate-x-5' : 'translate-x-1'}`}
                  />
                </button>
              </div>
            </div>

            {/* Action Trigger Buttons */}
            <div className="flex space-x-3">
              {!isSolving ? (
                <button
                  id="btn-solve-start"
                  onClick={startSolver}
                  className="flex-1 flex items-center justify-center space-x-2 py-2.5 px-4 bg-blue-600 text-white font-medium text-sm rounded-lg hover:bg-blue-700 active:bg-blue-800 transition-colors shadow-sm"
                >
                  <Play className="h-4 w-4 fill-current" />
                  <span>Start Solving</span>
                </button>
              ) : (
                <button
                  id="btn-solve-stop"
                  onClick={stopSolver}
                  className="flex-1 flex items-center justify-center space-x-2 py-2.5 px-4 bg-red-600 text-white font-medium text-sm rounded-lg hover:bg-red-700 active:bg-red-800 transition-colors shadow-sm"
                >
                  <RotateCcw className="h-4 w-4" />
                  <span>Interrupt Solver</span>
                </button>
              )}
            </div>
          </div>

          {/* Section 2: Instance Summary Details */}
          {instanceData && (
            <div className="bg-white border border-slate-200 rounded-xl shadow-sm p-5 flex flex-col" id="panel-instance-details">
              <div className="flex items-center space-x-2 border-b border-slate-100 pb-3 mb-4">
                <FileText className="h-4 w-4 text-blue-600" />
                <h2 className="text-sm font-bold uppercase tracking-wider text-slate-500">Instance Specification</h2>
              </div>
              
              <div className="grid grid-cols-2 gap-y-3 gap-x-4 text-sm">
                <div>
                  <span className="block text-[11px] font-bold uppercase tracking-wider text-slate-400">Name</span>
                  <span className="font-semibold text-slate-700">{instanceData.name}</span>
                </div>
                <div>
                  <span className="block text-[11px] font-bold uppercase tracking-wider text-slate-400">Capacity</span>
                  <span className="font-semibold text-slate-700">{instanceData.capacity} u</span>
                </div>
                <div>
                  <span className="block text-[11px] font-bold uppercase tracking-wider text-slate-400">Customers</span>
                  <span className="font-semibold text-slate-700">{instanceData.customers.length}</span>
                </div>
                <div>
                  <span className="block text-[11px] font-bold uppercase tracking-wider text-slate-400">Total Vehicles</span>
                  <span className="font-semibold text-slate-700">{instanceData.vehicleNumber} avail</span>
                </div>
              </div>

              {/* Depot Information */}
              <div className="mt-4 pt-4 border-t border-slate-100 flex items-start space-x-3 text-xs">
                <MapPin className="h-4 w-4 text-red-500 shrink-0 mt-0.5" />
                <div>
                  <span className="font-bold text-slate-700 block">Depot Location</span>
                  <span className="text-slate-500">X: {instanceData.depot.x}, Y: {instanceData.depot.y}</span>
                  <span className="text-slate-500 block">Time Window: [{instanceData.depot.readyTime}, {instanceData.depot.dueDate}]</span>
                </div>
              </div>

              {/* Best Known Optimal reference details */}
              {BEST_KNOWN_SOLUTIONS[selectedInstanceId] && (
                <div className="mt-4 pt-4 border-t border-slate-100 flex flex-col text-xs" id="optimal-reference-section">
                  <div className="flex items-center justify-between mb-2">
                    <span className="font-bold text-slate-700 block">Best Known Optimal</span>
                    <span className="font-mono text-slate-600 bg-slate-100 px-1.5 py-0.5 rounded text-[10px]">Reference</span>
                  </div>
                  <div className="grid grid-cols-2 gap-2 text-slate-600">
                    <div>Distance: <span className="font-semibold text-slate-800">{BEST_KNOWN_SOLUTIONS[selectedInstanceId].distance}</span></div>
                    <div>Vehicles: <span className="font-semibold text-slate-800">{BEST_KNOWN_SOLUTIONS[selectedInstanceId].vehicles}</span></div>
                  </div>
                  {solution && (() => {
                    const gap = computeGapToOptimal(solution, BEST_KNOWN_SOLUTIONS[selectedInstanceId]);
                    return (
                      <div className="mt-2 text-[10px] text-slate-500 flex justify-between items-center bg-slate-50/50 p-1.5 rounded border border-slate-100">
                        <span>Gap to Optimal:</span>
                        <span className={`font-mono font-bold ${gap.tone === 'good' ? 'text-green-600' : 'text-amber-600'}`}>
                          {gap.text}
                        </span>
                      </div>
                    );
                  })()}
                  {OPTIMAL_SOLUTIONS[selectedInstanceId] && (
                    <button
                      id="btn-view-optimal-ref"
                      onClick={loadOptimalReference}
                      className={`mt-3 w-full flex items-center justify-center space-x-2 py-1.5 px-2.5 border border-slate-200 text-[11px] font-semibold rounded-lg shadow-sm transition-colors ${
                        isViewingOptimal 
                          ? 'bg-blue-50 text-blue-700 border-blue-200 hover:bg-blue-100' 
                          : 'bg-white text-slate-700 hover:bg-slate-50'
                      }`}
                    >
                      <span>{isViewingOptimal ? 'Viewing Optimal Reference ✔' : 'Load Optimal Sequence'}</span>
                    </button>
                  )}
                </div>
              )}
            </div>
          )}
        </div>

        {/* RIGHT COLUMN: Main Content Stack (Routing Visualization, Route Explorer, Status & History) */}
        <div className="lg:col-span-9 flex flex-col gap-6 overflow-hidden">

        {/* Section 1: SVG Routing Visualization Map */}
        <div className="flex flex-col bg-white border border-slate-200 rounded-xl shadow-sm overflow-hidden min-h-[500px]">
          <div className="px-5 py-4 border-b border-slate-100 flex items-center justify-between bg-white" id="panel-map-header">
            <div className="flex items-center space-x-2">
              <Layers className="h-4 w-4 text-blue-600" />
              <h2 className="text-sm font-bold uppercase tracking-wider text-slate-500">Routing Visualization</h2>
            </div>
            {selectedRouteId !== null && (
              <button 
                id="btn-clear-route-filter"
                onClick={() => setSelectedRouteId(null)}
                className="text-xs text-blue-600 font-medium hover:underline"
              >
                Show All Routes
              </button>
            )}
          </div>

          <div className="flex-1 relative bg-slate-50/20 p-4 select-none flex items-center justify-center">
            {instanceData ? (
              <svg 
                className="w-full h-full max-h-[600px] overflow-visible" 
                viewBox={`${bounds.minX} ${bounds.minY} ${bounds.width} ${bounds.height}`}
              >
                {/* SVG Marker definitions for arrows */}
                <defs>
                  {ROUTE_COLORS.map((color, idx) => (
                    <marker
                      key={`arrow-${idx}`}
                      id={`arrow-${idx}`}
                      viewBox="0 0 10 10"
                      refX="11.5" // Scale down refX for smaller nodes (0.7 radius)
                      refY="5"
                      markerWidth="2.5"
                      markerHeight="2.5"
                      orient="auto-start-reverse"
                    >
                      <path d="M 0 1.5 L 8 5 L 0 8.5 z" fill={color} />
                    </marker>
                  ))}
                  {/* Generic arrow marker */}
                  <marker
                    id="arrow-generic"
                    viewBox="0 0 10 10"
                    refX="11.5"
                    refY="5"
                    markerWidth="2.5"
                    markerHeight="2.5"
                    orient="auto-start-reverse"
                  >
                    <path d="M 0 1.5 L 8 5 L 0 8.5 z" fill="#94a3b8" />
                  </marker>
                </defs>

                {/* 1. DRAW ROUTES PATHS */}
                {solution?.routes.map((route, routeIdx) => {
                  const isRouteSelected = selectedRouteId === null || selectedRouteId === route.vehicleId;
                  if (!isRouteSelected) return null;

                  const color = ROUTE_COLORS[routeIdx % ROUTE_COLORS.length];
                  
                  // Construct multi-node path starting and ending at the depot
                  const points: string[] = [];
                  points.push(`${instanceData.depot.x},${instanceData.depot.y}`);
                  route.customerIds.forEach(id => {
                    const cust = getCustomer(id);
                    if (cust) points.push(`${cust.x},${cust.y}`);
                  });
                  points.push(`${instanceData.depot.x},${instanceData.depot.y}`);

                  // Draw each leg to add arrows
                  const isRouteHovered = hoveredRoute?.vehicleId === route.vehicleId;
                  return (
                    <g 
                      key={`route-path-${route.vehicleId}`} 
                      style={{ opacity: isRouteSelected ? 1 : 0.15 }}
                    >
                      {points.map((pt, ptIdx) => {
                        if (ptIdx === points.length - 1) return null;
                        const [x1, y1] = pt.split(',').map(parseFloat);
                        const [x2, y2] = points[ptIdx + 1].split(',').map(parseFloat);

                        return (
                          <line
                            key={`leg-${route.vehicleId}-${ptIdx}`}
                            x1={x1}
                            y1={y1}
                            x2={x2}
                            y2={y2}
                            stroke={isRouteHovered ? "#f59e0b" : color} // Highlight with golden-amber on hover
                            strokeWidth={
                              selectedRouteId === route.vehicleId 
                                ? "1.0" 
                                : isRouteHovered 
                                  ? "0.8" 
                                  : "0.4"
                            }
                            markerEnd={`url(#arrow-${routeIdx % ROUTE_COLORS.length})`}
                            className="transition-all cursor-pointer"
                            onClick={() => setSelectedRouteId(route.vehicleId)}
                            onMouseEnter={() => setHoveredRoute(route)}
                            onMouseLeave={() => setHoveredRoute(null)}
                          />
                        );
                      })}
                    </g>
                  );
                })}

                {/* 2. DRAW CUSTOMERS */}
                {instanceData.customers.map(cust => {
                  // Check if customer belongs to the currently active route if filtered
                  let belongsToActiveRoute = true;
                  let customerColor = '#475569'; // default slate-600
                  let rIdx = -1;

                  if (solution) {
                    const r = solution.routes.find(route => route.customerIds.includes(cust.id));
                    if (r) {
                      rIdx = solution.routes.indexOf(r);
                      customerColor = ROUTE_COLORS[rIdx % ROUTE_COLORS.length];
                    }
                    if (selectedRouteId !== null) {
                      belongsToActiveRoute = selectedRoute?.customerIds.includes(cust.id) || false;
                    }
                  }

                  const isHovered = hoveredCustomer?.id === cust.id;

                  return (
                    <circle
                      key={`customer-${cust.id}`}
                      cx={cust.x}
                      cy={cust.y}
                      r={isHovered ? "1.5" : "0.7"}
                      fill={belongsToActiveRoute ? customerColor : "#cbd5e1"}
                      stroke="#ffffff"
                      strokeWidth="0.15"
                      className="cursor-pointer transition-all duration-150"
                      style={{ opacity: belongsToActiveRoute ? 1 : 0.25 }}
                      onMouseEnter={() => {
                        setHoveredCustomer(cust);
                        if (solution) {
                          const r = solution.routes.find(route => route.customerIds.includes(cust.id));
                          if (r) setHoveredRoute(r);
                        }
                      }}
                      onMouseLeave={() => {
                        setHoveredCustomer(null);
                        setHoveredRoute(null);
                      }}
                    />
                  );
                })}

                {/* 3. DRAW DEPOT */}
                <rect
                  x={instanceData.depot.x - 0.9}
                  y={instanceData.depot.y - 0.9}
                  width="1.8"
                  height="1.8"
                  fill="#dc2626" // bold red
                  stroke="#ffffff"
                  strokeWidth="0.2"
                  className="cursor-pointer transition-transform duration-150"
                  onMouseEnter={() => setHoveredCustomer(instanceData.depot)}
                  onMouseLeave={() => setHoveredCustomer(null)}
                />
              </svg>
            ) : (
              <div className="flex flex-col items-center text-slate-400 space-y-2">
                <Info className="h-8 w-8 text-slate-300" />
                <span className="text-sm">Instance visualization will appear here.</span>
              </div>
            )}

            {/* HOVER CUSTOMER TOOLTIP */}
            {hoveredCustomer && (
              <div className="absolute top-4 left-4 bg-white/95 backdrop-blur border border-slate-200 shadow-lg rounded-lg p-3 max-w-[220px] pointer-events-none z-10 transition-all duration-150" id="map-tooltip">
                <div className="font-bold text-slate-800 text-xs flex items-center justify-between border-b border-slate-100 pb-1 mb-1.5">
                  <span>{hoveredCustomer.id === 0 ? 'Depot' : `Customer #${hoveredCustomer.id}`}</span>
                  <span className="text-[10px] text-slate-400">({hoveredCustomer.x.toFixed(0)}, {hoveredCustomer.y.toFixed(0)})</span>
                </div>
                <div className="space-y-1 text-[11px] text-slate-600">
                  <div className="flex justify-between">
                    <span>Demand:</span>
                    <span className="font-semibold text-slate-800">{hoveredCustomer.demand} u</span>
                  </div>
                  <div className="flex justify-between">
                    <span>Ready Time:</span>
                    <span className="font-semibold text-slate-800">{hoveredCustomer.readyTime}</span>
                  </div>
                  <div className="flex justify-between">
                    <span>Due Date:</span>
                    <span className="font-semibold text-slate-800">{hoveredCustomer.dueDate}</span>
                  </div>
                  <div className="flex justify-between">
                    <span>Service Time:</span>
                    <span className="font-semibold text-slate-800">{hoveredCustomer.serviceTime}</span>
                  </div>
                  {solution && hoveredCustomer.id !== 0 && (
                    <div className="mt-1.5 pt-1.5 border-t border-slate-100">
                      {(() => {
                        const route = solution.routes.find(r => r.customerIds.includes(hoveredCustomer.id));
                        if (route) {
                          const arr = route.arrivalTimes[hoveredCustomer.id] ?? 0;
                          const wait = route.waitingTimes[hoveredCustomer.id] ?? 0;
                          return (
                            <div className="space-y-0.5 text-[10px]">
                              <div className="flex justify-between text-blue-600 font-medium">
                                <span>Assigned Truck:</span>
                                <span>Vehicle #{route.vehicleId}</span>
                              </div>
                              <div className="flex justify-between">
                                <span>Arrival Time:</span>
                                <span className="font-medium text-slate-700">{arr}</span>
                              </div>
                              {wait > 0 && (
                                <div className="flex justify-between text-amber-600">
                                  <span>Waiting Time:</span>
                                  <span className="font-medium">{wait}</span>
                                </div>
                              )}
                            </div>
                          );
                        }
                        return <span className="text-[10px] text-red-500">Unassigned / Not visited</span>;
                      })()}
                    </div>
                  )}
                </div>
              </div>
            )}

            {/* HOVER ROUTE TOOLTIP */}
            {hoveredRoute && (
              <div className="absolute top-4 right-4 bg-white/95 backdrop-blur border border-slate-200 shadow-lg rounded-lg p-3 w-[220px] pointer-events-none z-10 transition-all duration-150" id="route-tooltip">
                <div className="font-bold text-slate-800 text-xs flex items-center justify-between border-b border-slate-100 pb-1 mb-1.5">
                  <div className="flex items-center space-x-1.5">
                    <span className="w-2.5 h-2.5 rounded-full" style={{ backgroundColor: ROUTE_COLORS[(hoveredRoute.vehicleId - 1) % ROUTE_COLORS.length] }}></span>
                    <span>Vehicle #{hoveredRoute.vehicleId} Tour</span>
                  </div>
                  <span className="text-[10px] text-blue-600 font-semibold uppercase tracking-wider">Truck</span>
                </div>
                <div className="space-y-1 text-[11px] text-slate-600">
                  <div className="flex justify-between">
                    <span>Total Distance:</span>
                    <span className="font-semibold text-slate-800">{hoveredRoute.distance.toFixed(2)}</span>
                  </div>
                  <div className="flex justify-between">
                    <span>Capacity Load:</span>
                    <span className="font-semibold text-slate-800">
                      {hoveredRoute.load} / {instanceData?.capacity ?? 200}
                    </span>
                  </div>
                  <div className="flex justify-between">
                    <span>Stops Visited:</span>
                    <span className="font-semibold text-slate-800">{hoveredRoute.customerIds.length} stops</span>
                  </div>
                  <div className="flex flex-col pt-1.5 border-t border-slate-100">
                    <span className="text-[10px] text-slate-400 uppercase tracking-wider font-semibold mb-1">Route Sequence:</span>
                    <div className="bg-slate-50/80 p-1 rounded text-[10px] font-mono text-slate-700 leading-normal max-h-[80px] overflow-y-auto break-words">
                      Depot → {hoveredRoute.customerIds.join(' → ')} → Depot
                    </div>
                  </div>
                </div>
              </div>
            )}
          </div>
        </div>

        {/* Section 2: Route Explorer (Route Inspector & Time Windows gantt timeline) */}
        <div className="flex flex-col gap-6 overflow-hidden">
          {/* Section 1: Route Inspector Header & List */}
          <div className="bg-white border border-slate-200 rounded-xl shadow-sm p-5 flex flex-col max-h-[250px] overflow-hidden" id="panel-routes-list">
            <div className="flex items-center space-x-2 border-b border-slate-100 pb-3 mb-3">
              <Truck className="h-4 w-4 text-blue-600" />
              <h2 className="text-sm font-bold uppercase tracking-wider text-slate-500">Route Explorer</h2>
            </div>

            <div className="flex-1 overflow-y-auto space-y-1.5 pr-1" id="routes-scroll-container">
              {solution?.routes.map((route, idx) => {
                const color = ROUTE_COLORS[idx % ROUTE_COLORS.length];
                const isSelected = selectedRouteId === route.vehicleId;
                const capPercent = (route.load / (instanceData?.capacity || 100)) * 100;

                return (
                  <div
                    key={route.vehicleId}
                    id={`route-card-${route.vehicleId}`}
                    onClick={() => setSelectedRouteId(isSelected ? null : route.vehicleId)}
                    className={`cursor-pointer border rounded-lg p-2.5 transition-all text-xs flex flex-col ${isSelected ? 'border-slate-300 bg-slate-50 shadow-sm' : 'border-slate-100 bg-white hover:bg-slate-50/50'}`}
                  >
                    <div className="flex items-center justify-between mb-1.5">
                      <div className="flex items-center space-x-2">
                        <span className="w-2.5 h-2.5 rounded-full" style={{ backgroundColor: color }}></span>
                        <span className="font-bold text-slate-700">Vehicle #{route.vehicleId}</span>
                      </div>
                      <span className="text-slate-500 text-[10px]">{route.customerIds.length} stops</span>
                    </div>

                    <div className="flex justify-between text-[10px] text-slate-500 mb-1">
                      <span>Dist: {route.distance.toFixed(1)}</span>
                      <span>Load: {route.load}/{instanceData?.capacity || 100}</span>
                    </div>

                    {/* Simple load bar */}
                    <div className="w-full bg-slate-100 rounded-full h-1 overflow-hidden">
                      <div 
                        className={`h-full rounded-full ${capPercent > 90 ? 'bg-red-500' : 'bg-blue-500'}`}
                        style={{ width: `${Math.min(100, capPercent)}%` }}
                      ></div>
                    </div>
                  </div>
                );
              }) || (
                <div className="text-center text-slate-400 py-6 text-xs">
                  Run solver to generate routes.
                </div>
              )}
            </div>
          </div>

          {/* Section 2: Detailed Visited Sequence & Time-Window Gantt Timeline */}
          {selectedRoute && instanceData && (
            <div className="bg-white border border-slate-200 rounded-xl shadow-sm p-5 flex-1 flex flex-col overflow-hidden" id="panel-route-details">
              <div className="flex items-center justify-between border-b border-slate-100 pb-3 mb-4">
                <span className="text-sm font-bold uppercase tracking-wider text-slate-500">Vehicle #{selectedRoute.vehicleId} Timeline</span>
                <span className="text-[10px] bg-emerald-50 text-emerald-700 border border-emerald-200 rounded-full px-2 py-0.5">Feasible</span>
              </div>

              {/* Sequential timeline with ready/due date constraint markers */}
              <div className="flex-1 overflow-y-auto space-y-4 pr-1" id="route-gantt-container">
                {/* Seed Depot Start */}
                <div className="flex items-start space-x-3 text-xs border-l-2 border-slate-200 pl-3 relative ml-2">
                  <div className="absolute -left-[5px] top-1.5 w-2 h-2 rounded-full bg-slate-300"></div>
                  <div className="flex-1">
                    <div className="flex justify-between font-bold text-slate-700">
                      <span>Depot Start</span>
                      <span className="text-slate-400 font-normal">Depart: 0.0</span>
                    </div>
                  </div>
                </div>

                {/* Customers sequence list */}
                {selectedRoute.customerIds.map((cID, seqIdx) => {
                  const cust = getCustomer(cID);
                  if (!cust) return null;

                  const arrTime = selectedRoute.arrivalTimes[cID] || 0;
                  const waitTime = selectedRoute.waitingTimes[cID] || 0;
                  const depTime = selectedRoute.departureTimes[cID] || 0;
                  const ready = cust.readyTime;
                  const due = cust.dueDate;

                  // Let's compute percentages for a mini visual timeline bar
                  // Timeline covers interval [ready - buffer, due + buffer]
                  const minBound = Math.max(0, ready - (due - ready) * 0.2);
                  const maxBound = due + (due - ready) * 0.2;
                  const totalSpan = maxBound - minBound || 1;

                  const windowLeft = ((ready - minBound) / totalSpan) * 100;
                  const windowWidth = ((due - ready) / totalSpan) * 100;
                  const arrPoint = ((arrTime - minBound) / totalSpan) * 100;
                  
                  return (
                    <div key={`seq-${cID}-${seqIdx}`} className="flex items-start space-x-3 text-xs border-l-2 border-blue-500 pl-3 relative ml-2">
                      <div className="absolute -left-[5px] top-1.5 w-2.5 h-2.5 rounded-full bg-blue-600 border border-white"></div>
                      
                      <div className="flex-1">
                        <div className="flex justify-between font-semibold text-slate-800">
                          <span>Cust #{cID}</span>
                          <span className="font-mono text-[10px] text-slate-500">Arr: {arrTime.toFixed(1)}</span>
                        </div>

                        {/* Extra details like wait times */}
                        {waitTime > 0 && (
                          <div className="text-[10px] text-amber-600 font-medium">
                            Waiting: {waitTime.toFixed(1)} (Ready: {ready})
                          </div>
                        )}

                        {/* Time window Gantt block */}
                        <div className="mt-1.5 mb-1 bg-slate-100 rounded h-3.5 relative overflow-hidden border border-slate-200/50">
                          {/* Ready-due date window block */}
                          <div 
                            className="absolute bg-blue-100/60 border-x border-blue-300 h-full text-[8px] flex items-center justify-center font-bold text-blue-800"
                            style={{ left: `${windowLeft}%`, width: `${windowWidth}%` }}
                          >
                            T
                          </div>
                          {/* Arrival marker */}
                          <div 
                            className={`absolute w-1.5 h-full z-10 ${waitTime > 0 ? 'bg-amber-500' : 'bg-green-500'}`}
                            style={{ left: `${arrPoint}%` }}
                            title={`Arrived at ${arrTime.toFixed(1)}`}
                          ></div>
                        </div>

                        <div className="flex justify-between text-[9px] text-slate-400">
                          <span>Ready: {ready}</span>
                          <span>Due: {due}</span>
                          <span>Depart: {depTime.toFixed(1)}</span>
                        </div>
                      </div>
                    </div>
                  );
                })}

                {/* Return to Depot end node */}
                <div className="flex items-start space-x-3 text-xs pl-3 relative ml-2">
                  <div className="absolute -left-[5px] top-1.5 w-2 h-2 rounded-full bg-red-600"></div>
                  <div className="flex-1">
                    <div className="font-bold text-slate-700">
                      <span>Depot Return</span>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          )}
        </div>

          {/* Section 3: Solver Progress & Convergence History */}
          <div className="bg-white border border-slate-200 rounded-xl shadow-sm p-5 flex-1 flex flex-col" id="panel-solver-status">
            <div className="flex items-center justify-between border-b border-slate-100 pb-3 mb-4">
              <div className="flex items-center space-x-2">
                <Clock className="h-4 w-4 text-blue-600" />
                <h2 className="text-sm font-bold uppercase tracking-wider text-slate-500">Status & History</h2>
              </div>
              {isSolving && (
                <span className="inline-flex h-2 w-2 relative rounded-full bg-emerald-500">
                  <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75"></span>
                </span>
              )}
            </div>

            {/* Active message log / Rich Terminal Console */}
            <div className="flex flex-col flex-1 min-h-[380px] border border-slate-200 rounded-lg overflow-hidden bg-slate-900 text-slate-100 font-mono text-xs mb-4">
              {/* Terminal Header & Filter Bar */}
              <div className="bg-slate-800 border-b border-slate-700 px-3 py-2 flex flex-col gap-2">
                <div className="flex items-center justify-between text-[11px] text-slate-400">
                  <div className="flex items-center space-x-1.5 font-bold">
                    <span className="w-2.5 h-2.5 rounded-full bg-red-500 inline-block"></span>
                    <span className="w-2.5 h-2.5 rounded-full bg-yellow-500 inline-block"></span>
                    <span className="w-2.5 h-2.5 rounded-full bg-emerald-500 inline-block"></span>
                    <span className="ml-1 text-slate-300">SOLVER CONSOLE</span>
                  </div>
                  {isSolving && <span className="animate-pulse text-emerald-400 font-medium">Solving (Elapsed: {elapsedTime}s)</span>}
                </div>

                {/* Filter Pills */}
                <div className="flex flex-wrap gap-1 text-[10px]">
                  {(['all', 'improvements', 'llm', 'lns'] as const).map((filter) => {
                    const label = {
                      all: 'All',
                      improvements: '🏆 Improvements',
                      llm: '🧠 Smart Heuristic',
                      lns: '⚡ LNS',
                    }[filter];

                    const activeStyle = {
                      all: 'bg-slate-700 text-white',
                      improvements: 'bg-emerald-800/80 text-emerald-200 border-emerald-700/50',
                      llm: 'bg-violet-900 text-violet-200 border-violet-800',
                      lns: 'bg-blue-900/60 text-blue-200 border-blue-800',
                    }[filter];

                    return (
                      <button
                        key={filter}
                        onClick={() => setLogFilter(filter)}
                        className={`px-2 py-0.5 rounded border transition-colors ${logFilter === filter ? activeStyle : 'bg-slate-800/40 text-slate-400 border-slate-800/80 hover:bg-slate-800 hover:text-slate-200'}`}
                      >
                        {label}
                      </button>
                    );
                  })}
                </div>
              </div>

              {/* Log Scroll Box */}
              <div 
                ref={logScrollRef}
                className="flex-1 p-3 overflow-y-auto space-y-1.5 font-mono text-[11px] leading-relaxed max-h-[300px] h-[300px] bg-slate-950/95"
              >
                {solverLogs.length === 0 ? (
                  <div className="text-slate-500 italic h-full flex items-center justify-center">
                    Ready. Click 'Start Optimization' to watch decision traces...
                  </div>
                ) : (
                  (() => {
                    // Filter logs based on selection
                    const filtered = solverLogs.filter(log => {
                      if (logFilter === 'all') return true;
                      if (logFilter === 'improvements') {
                        return log.includes('[NEW BEST]') || log.includes('[SUCCESS]') || log.includes('0.1%') || log.includes('Initial solution') || log.includes('started') || log.includes('results');
                      }
                      if (logFilter === 'llm') {
                        return log.includes('[HEURISTIC:') || log.includes('[LKH:') || log.includes('Heuristic') || log.includes('LKH') || log.includes('Bypassing') || log.includes('Attempt');
                      }
                      if (logFilter === 'lns') {
                        return log.includes('[LNS:') || log.includes('Candidate');
                      }
                      return true;
                    });

                    if (filtered.length === 0) {
                      return <div className="text-slate-600 italic text-center pt-8">No logs match this filter.</div>;
                    }

                    return filtered.map((log, index) => {
                      // Attempt to parse category: e.g. [LNS:CHOOSE] Message
                      const categoryMatch = log.match(/^\[(.*?)\] (.*)$/);
                      if (categoryMatch) {
                        const category = categoryMatch[1];
                        const text = categoryMatch[2];

                        // Style category badge
                        let badgeStyle = "bg-slate-800 text-slate-400 border-slate-700";
                        let textStyle = "text-slate-300";

                        if (category === 'LNS:CHOOSE') {
                          badgeStyle = "bg-slate-900 text-slate-500 border-slate-800";
                          textStyle = "text-slate-400";
                        } else if (category === 'LNS:ACCEPT') {
                          badgeStyle = "bg-emerald-950 text-emerald-400 border-emerald-900";
                          textStyle = "text-slate-300";
                        } else if (category === 'LNS:REJECT') {
                          badgeStyle = "bg-slate-900 text-slate-600 border-slate-900";
                          textStyle = "text-slate-500";
                        } else if (category === 'LNS:DECISION') {
                          if (text.includes('[NEW BEST]')) {
                            badgeStyle = "bg-emerald-500 text-white font-bold border-emerald-400";
                            textStyle = "text-emerald-300 font-bold";
                          } else {
                            badgeStyle = "bg-emerald-900 text-emerald-300 border-emerald-800";
                            textStyle = "text-slate-300";
                          }
                        } else if (category === 'HEURISTIC:TRIGGER') {
                          badgeStyle = "bg-violet-950 text-violet-400 border-violet-900";
                          textStyle = "text-violet-300";
                        } else if (category === 'HEURISTIC:DECISION') {
                          badgeStyle = "bg-violet-900 text-violet-200 border-violet-800";
                          textStyle = "text-violet-100 font-semibold";
                        } else if (category === 'HEURISTIC:SUB-SOLVER') {
                          badgeStyle = "bg-indigo-950 text-indigo-400 border-indigo-900";
                          textStyle = "text-slate-300";
                        } else if (category === 'HEURISTIC:MERGE') {
                          badgeStyle = "bg-blue-950 text-blue-400 border-blue-950";
                          textStyle = "text-slate-400";
                        } else if (category === 'HEURISTIC:SUCCESS') {
                          badgeStyle = "bg-emerald-500 text-white font-bold border-emerald-400";
                          textStyle = "text-emerald-300 font-bold animate-pulse";
                        } else if (category === 'HEURISTIC:FAILURE') {
                          badgeStyle = "bg-rose-950 text-rose-400 border-rose-900";
                          textStyle = "text-rose-300/80";
                        } else if (category === 'LKH:TRIGGER') {
                          badgeStyle = "bg-cyan-950 text-cyan-400 border-cyan-900";
                          textStyle = "text-cyan-300";
                        } else if (category === 'LKH:COMPARE') {
                          badgeStyle = "bg-cyan-900 text-cyan-200 border-cyan-800";
                          textStyle = "text-slate-300";
                        } else if (category === 'LKH:SUCCESS') {
                          badgeStyle = "bg-emerald-500 text-white font-bold border-emerald-400";
                          textStyle = "text-emerald-300 font-bold animate-pulse";
                        } else if (category === 'LKH:FALLBACK') {
                          badgeStyle = "bg-rose-950 text-rose-400 border-rose-900";
                          textStyle = "text-rose-300/80";
                        }

                        return (
                          <div key={index} className="flex items-start gap-1.5 hover:bg-slate-900/50 py-0.5 px-1 rounded transition-colors">
                            <span className={`px-1.5 py-0.5 rounded text-[9px] uppercase tracking-wider border font-bold ${badgeStyle}`}>
                              {category}
                            </span>
                            <span className={textStyle}>{text}</span>
                          </div>
                        );
                      }

                      // Default fallbacks (errors, starts)
                      const isError = log.toLowerCase().includes('error');
                      const isHeuristicOrLkh = log.includes('Heuristic') || log.includes('LKH');
                      const defaultClass = isError
                        ? 'text-red-400 font-bold'
                        : isHeuristicOrLkh
                          ? 'text-violet-300 font-medium'
                          : 'text-slate-400';

                      return (
                        <div key={index} className={`flex items-start space-x-1.5 py-0.5 px-1 ${defaultClass}`}>
                          <span className="text-slate-600">›</span>
                          <span>{log}</span>
                        </div>
                      );
                    });
                  })()
                )}
              </div>
            </div>

            {/* Best Solution metrics */}
            {solution && (
              <div className="grid grid-cols-2 gap-3 mb-4">
                <div className="bg-slate-50 rounded-lg p-2.5 border border-slate-100">
                  <span className="block text-[10px] font-bold uppercase text-slate-400">Vehicles Used</span>
                  <span className="text-lg font-bold text-slate-800 flex items-center space-x-1.5">
                    <Truck className="h-4 w-4 text-slate-500" />
                    <span>{solution.totalVehicles}</span>
                  </span>
                </div>
                <div className="bg-slate-50 rounded-lg p-2.5 border border-slate-100">
                  <span className="block text-[10px] font-bold uppercase text-slate-400">Best Distance</span>
                  <span className="text-lg font-bold text-slate-800 flex items-center space-x-1.5">
                    <TrendingDown className="h-4 w-4 text-slate-500" />
                    <span>{solution.totalDistance.toFixed(2)}</span>
                  </span>
                </div>
              </div>
            )}

            {solution && (
              <button
                id="btn-export-solution"
                onClick={() => exportSolutionAsText(solution, selectedInstanceId, instanceData?.name ?? selectedInstanceId)}
                className="w-full flex items-center justify-center space-x-2 py-2 px-3 mb-4 text-sm font-medium text-slate-700 bg-white border border-slate-300 rounded-lg hover:bg-slate-50 transition-colors"
              >
                <FileText className="h-4 w-4" />
                <span>Export Result</span>
              </button>
            )}

            {/* Custom SVG Sparkline for Convergence history */}
            {progressHistory.length > 1 && (
              <div className="flex-1 flex flex-col min-h-[180px]">
                <span className="text-[10px] font-bold uppercase tracking-wider text-slate-400 mb-2 flex items-center justify-between">
                  <span>Convergence Curve</span>
                  <span className="font-normal text-slate-500">Distance vs Iteration</span>
                </span>
                <div className="flex-1 bg-slate-50/50 rounded-lg border border-slate-200 relative p-3 min-h-[140px] flex items-center justify-center">
                  <svg className="w-full h-full max-h-[160px]" viewBox="0 0 240 120" id="svg-convergence-curve">
                    {(() => {
                      const maxDist = Math.max(...progressHistory.map(p => p.distance));
                      const minDist = Math.min(...progressHistory.map(p => p.distance));
                      const distDiff = maxDist - minDist || 1;
                      const iterMax = Math.max(...progressHistory.map(p => p.iteration)) || 1;
                      const iterMin = Math.min(...progressHistory.map(p => p.iteration)) || 0;
                      const iterDiff = iterMax - iterMin || 1;

                      const points = progressHistory.map((p) => {
                        const x = 38 + ((p.iteration - iterMin) / iterDiff) * 187;
                        const y = 95 - ((p.distance - minDist) / distDiff) * 80;
                        return `${x.toFixed(1)},${y.toFixed(1)}`;
                      }).join(' ');

                      const startY = 95 - ((progressHistory[0].distance - minDist) / distDiff) * 80;
                      const endY = 95 - ((progressHistory[progressHistory.length - 1].distance - minDist) / distDiff) * 80;

                      return (
                        <>
                          {/* Grid lines */}
                          <line x1="38" y1="15" x2="225" y2="15" stroke="#f1f5f9" strokeWidth="1" strokeDasharray="2,2" />
                          <line x1="38" y1="55" x2="225" y2="55" stroke="#f1f5f9" strokeWidth="1" strokeDasharray="2,2" />
                          <line x1="131.5" y1="15" x2="131.5" y2="95" stroke="#f1f5f9" strokeWidth="1" strokeDasharray="2,2" />

                          {/* Axes */}
                          <line x1="38" y1="15" x2="38" y2="95" stroke="#cbd5e1" strokeWidth="1" />
                          <line x1="38" y1="95" x2="225" y2="95" stroke="#cbd5e1" strokeWidth="1" />

                          {/* Y-Ticks */}
                          <line x1="34" y1="15" x2="38" y2="15" stroke="#cbd5e1" strokeWidth="1" />
                          <line x1="34" y1="55" x2="38" y2="55" stroke="#cbd5e1" strokeWidth="1" />
                          <line x1="34" y1="95" x2="38" y2="95" stroke="#cbd5e1" strokeWidth="1" />

                          {/* Y-labels */}
                          <text x="31" y="18" textAnchor="end" className="text-[7.5px] fill-slate-500 font-mono font-medium">{maxDist.toFixed(1)}</text>
                          <text x="31" y="58" textAnchor="end" className="text-[7.5px] fill-slate-500 font-mono font-medium">{(minDist + distDiff * 0.5).toFixed(1)}</text>
                          <text x="31" y="98" textAnchor="end" className="text-[7.5px] fill-slate-600 font-mono font-bold">{minDist.toFixed(1)}</text>

                          {/* X-Ticks */}
                          <line x1="38" y1="95" x2="38" y2="99" stroke="#cbd5e1" strokeWidth="1" />
                          <line x1="131.5" y1="95" x2="131.5" y2="99" stroke="#cbd5e1" strokeWidth="1" />
                          <line x1="225" y1="95" x2="225" y2="99" stroke="#cbd5e1" strokeWidth="1" />

                          {/* X-labels */}
                          <text x="38" y="107" textAnchor="middle" className="text-[7.5px] fill-slate-500 font-mono font-medium">{iterMin}</text>
                          <text x="131.5" y="107" textAnchor="middle" className="text-[7.5px] fill-slate-500 font-mono font-medium">{Math.round(iterMin + iterDiff * 0.5)}</text>
                          <text x="225" y="107" textAnchor="middle" className="text-[7.5px] fill-slate-500 font-mono font-medium">{iterMax}</text>

                          {/* Axis Titles */}
                          <text transform="translate(10, 55) rotate(-90)" textAnchor="middle" className="text-[7.5px] font-bold uppercase tracking-wider fill-slate-400">Distance</text>
                          <text x="131.5" y="117" textAnchor="middle" className="text-[7.5px] font-bold uppercase tracking-wider fill-slate-400">Iteration</text>

                          {/* Thinner line */}
                          <polyline
                            fill="none"
                            stroke="#3b82f6"
                            strokeWidth="1.2"
                            points={points}
                          />

                          {/* Starting and ending dots */}
                          <circle cx="38" cy={startY} r="2" fill="#ef4444" />
                          <circle cx="225" cy={endY} r="2" fill="#10b981" />
                        </>
                      );
                    })()}
                  </svg>
                </div>
              </div>
            )}
          </div>
        </div>
      </div>

      {/* MODAL WINDOW: Custom File Upload */}
      {showUploadModal && (
        <div className="fixed inset-0 bg-slate-900/50 backdrop-blur-sm flex items-center justify-center z-50 p-4 animate-fade-in" id="modal-upload">
          <div className="bg-white border border-slate-200 rounded-xl shadow-xl w-full max-w-lg overflow-hidden flex flex-col">
            <div className="px-6 py-4 border-b border-slate-100 flex items-center justify-between">
              <h3 className="text-base font-bold text-slate-900">Upload Solomon Benchmark Format File</h3>
              <button 
                id="btn-upload-close"
                onClick={() => setShowUploadModal(false)}
                className="text-slate-400 hover:text-slate-600 text-sm font-semibold"
              >
                ✕
              </button>
            </div>

            <div className="p-6 flex flex-col gap-4">
              {uploadError && (
                <div className="text-xs bg-red-50 text-red-700 border border-red-200 p-3 rounded-lg flex items-start space-x-2" id="upload-error-banner">
                  <AlertCircle className="h-4 w-4 shrink-0 mt-0.5" />
                  <span>{uploadError}</span>
                </div>
              )}

              <div>
                <label className="block text-xs font-semibold text-slate-600 mb-1" htmlFor="upload-name-input">Instance ID/Name</label>
                <input
                  id="upload-name-input"
                  type="text"
                  placeholder="e.g. c102, custom_r1"
                  value={uploadName}
                  onChange={(e) => setUploadName(e.target.value)}
                  className="w-full text-sm border border-slate-300 rounded-lg p-2 focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
                />
              </div>

              <div>
                <label className="block text-xs font-semibold text-slate-600 mb-1" htmlFor="upload-content-textarea">Solomon Format Data Contents</label>
                <textarea
                  id="upload-content-textarea"
                  rows={10}
                  placeholder="C101&#10;VEHICLE&#10;NUMBER     CAPACITY&#10;   25          200&#10;CUSTOMER&#10;CUST NO.  XCOORD.   YCOORD.    DEMAND   READY TIME  DUE DATE   SERVICE TIME&#10;    0          40         50          0          0       1236          0&#10;    1          45         68         10        912        967         90..."
                  value={uploadContent}
                  onChange={(e) => setUploadContent(e.target.value)}
                  className="w-full text-xs font-mono border border-slate-300 rounded-lg p-2 focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
                />
              </div>
            </div>

            <div className="px-6 py-4 border-t border-slate-100 bg-slate-50 flex justify-end space-x-3">
              <button
                id="btn-upload-cancel"
                onClick={() => setShowUploadModal(false)}
                className="px-4 py-2 text-sm font-medium text-slate-600 hover:text-slate-800"
              >
                Cancel
              </button>
              <button
                id="btn-upload-submit"
                onClick={handleUpload}
                className="px-4 py-2 text-sm font-medium text-white bg-blue-600 rounded-lg hover:bg-blue-700 active:bg-blue-800 transition-colors"
              >
                Upload & Parse
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
