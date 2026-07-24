import express from 'express';
import path from 'path';
import fs from 'fs';
import { spawn } from 'child_process';
import readline from 'readline';
import { createServer as createViteServer } from 'vite';
import { getOllamaConfigFromEnv, ollamaGenerateJSON } from './ollama_client';

const app = express();
const PORT = 3000;

app.use(express.json({ limit: '10mb' }));

// 1. Helper: Parse Solomon benchmark text format
interface Customer {
  id: number;
  x: number;
  y: number;
  demand: number;
  readyTime: number;
  dueDate: number;
  serviceTime: number;
}

function parseSolomonText(content: string) {
  const lines = content.split('\n');
  const customers: Customer[] = [];
  let name = '';
  let capacity = 200;
  let vehicleNumber = 25;
  let state = 0; // 0: header, 1: vehicle, 2: customer-header, 3: customer-data

  for (let line of lines) {
    line = line.trim();
    if (!line) continue;

    if (!name && state === 0) {
      name = line;
      continue;
    }

    if (line.includes('VEHICLE')) {
      state = 1;
      continue;
    }
    if (line.includes('CUSTOMER')) {
      state = 2;
      continue;
    }

    const fields = line.split(/\s+/);
    if (fields.length === 0) continue;

    if (state === 1) {
      if (fields[0] === 'NUMBER' || fields[0] === 'CAPACITY') continue;
      const num = parseInt(fields[0]);
      const capVal = parseFloat(fields[1]);
      if (!isNaN(num) && !isNaN(capVal)) {
        vehicleNumber = num;
        capacity = capVal;
        state = 0;
      }
    } else if (state === 2) {
      if (line.includes('CUST NO.') || line.includes('XCOORD')) continue;
      state = 3;
    }

    if (state === 3) {
      if (fields.length < 7) continue;
      const id = parseInt(fields[0]);
      const x = parseFloat(fields[1]);
      const y = parseFloat(fields[2]);
      const demand = parseFloat(fields[3]);
      const readyTime = parseFloat(fields[4]);
      const dueDate = parseFloat(fields[5]);
      const serviceTime = parseFloat(fields[6]);

      if (!isNaN(id) && !isNaN(x) && !isNaN(y) && !isNaN(demand) && !isNaN(readyTime) && !isNaN(dueDate) && !isNaN(serviceTime)) {
        customers.push({ id, x, y, demand, readyTime, dueDate, serviceTime });
      }
    }
  }

  const depot = customers.find(c => c.id === 0) || { id: 0, x: 40, y: 50, demand: 0, readyTime: 0, dueDate: 1236, serviceTime: 0 };
  const filteredCustomers = customers.filter(c => c.id !== 0);

  return {
    name,
    vehicleNumber,
    capacity,
    depot,
    customers: filteredCustomers
  };
}

// 2. API: Get list of instances
app.get('/api/instances', (req, res) => {
  const dirPath = path.join(process.cwd(), 'data');
  if (!fs.existsSync(dirPath)) {
    return res.json([]);
  }

  const files = fs.readdirSync(dirPath).filter(file => file.endsWith('.txt'));
  const instances = files.map(file => {
    const id = file.replace('.txt', '');
    const content = fs.readFileSync(path.join(dirPath, file), 'utf8');
    const parsed = parseSolomonText(content);
    return {
      id,
      name: parsed.name || id.toUpperCase(),
      customersCount: parsed.customers.length,
      capacity: parsed.capacity,
      vehicles: parsed.vehicleNumber
    };
  });

  res.json(instances);
});

// 3. API: Get details of a specific instance
app.get('/api/instances/:id', (req, res) => {
  const instanceId = req.params.id;
  const filePath = path.join(process.cwd(), 'data', `${instanceId}.txt`);

  if (!fs.existsSync(filePath)) {
    return res.status(404).json({ error: 'Instance not found' });
  }

  try {
    const content = fs.readFileSync(filePath, 'utf8');
    const parsed = parseSolomonText(content);
    res.json(parsed);
  } catch (error) {
    res.status(500).json({ error: 'Failed to parse instance file' });
  }
});

// 4. API: Upload raw custom Solomon text content
app.post('/api/upload', (req, res) => {
  const { name, content } = req.body;
  if (!name || !content) {
    return res.status(400).json({ error: 'Name and content are required' });
  }

  try {
    const safeName = name.replace(/[^a-zA-Z0-9_\-]/g, '').toLowerCase();
    const filePath = path.join(process.cwd(), 'data', `${safeName}.txt`);
    fs.mkdirSync(path.join(process.cwd(), 'data'), { recursive: true });
    fs.writeFileSync(filePath, content, 'utf8');

    // Test parse
    const parsed = parseSolomonText(content);
    res.json({
      success: true,
      id: safeName,
      name: parsed.name || safeName.toUpperCase(),
      customersCount: parsed.customers.length
    });
  } catch (error) {
    res.status(500).json({ error: 'Failed to process uploaded file' });
  }
});

// 5. API: Stream solve progress via SSE (Server-Sent Events)
const BACKEND_BEST_KNOWN_SOLUTIONS: Record<string, number> = {
  c101: 828.94,
  c201: 591.56,
  r101: 1645.79,
  r201: 1252.37,
  rc101: 1696.94,
  rc201: 1261.67
};

app.get('/api/solve-stream', async (req, res) => {
  const instanceId = req.query.instance as string;
  const iterations = req.query.iterations ? parseInt(req.query.iterations as string, 10) : 1000;
  const algorithm = (req.query.algorithm as string) || 'lns';
  const llmThreshold = req.query.llmThreshold ? parseInt(req.query.llmThreshold as string, 10) : 20;
  const optimal = req.query.optimal ? parseFloat(req.query.optimal as string) : undefined;
  const useLlm = req.query.useLlm === 'true';
  const useLkh = req.query.useLkh === 'true';

  if (!instanceId) {
    res.status(400).json({ error: 'Instance ID is required' });
    return;
  }

  const filePath = path.join(process.cwd(), 'data', `${instanceId}.txt`);
  if (!fs.existsSync(filePath)) {
    res.status(404).json({ error: 'Instance file not found' });
    return;
  }

  // Set response headers for SSE
  res.setHeader('Content-Type', 'text/event-stream');
  res.setHeader('Cache-Control', 'no-cache');
  res.setHeader('Connection', 'keep-alive');

  let cancelled = false;

  const args = [
    '-file', filePath,
    '-iterations', String(iterations),
    '-algorithm', algorithm,
    '-llm-threshold', String(llmThreshold),
    '-seed', String(Date.now()),
    `-use-llm=${useLlm}`,
    `-use-lkh=${useLkh}`
  ];
  if (optimal !== undefined && !isNaN(optimal)) {
    args.push('-optimal', String(optimal));
  }

  const solverBinPath = path.join(process.cwd(), 'solver_bin');
  const child = spawn(solverBinPath, args, { cwd: process.cwd() });

  req.on('close', () => {
    cancelled = true;
    child.kill();
  });

  const rl = readline.createInterface({ input: child.stdout });
  rl.on('line', (line) => {
    if (cancelled) return;
    const trimmed = line.trim();
    if (!trimmed) return;
    try {
      JSON.parse(trimmed);
    } catch {
      console.error('Non-JSON line from solver_bin, dropping:', trimmed);
      return;
    }
    res.write(`data: ${trimmed}\n\n`);
  });

  child.stderr.on('data', (data) => {
    console.error('[solver_bin stderr]', data.toString());
  });

  child.on('error', (err) => {
    console.error('Failed to spawn solver_bin:', err.message);
    if (!cancelled) {
      res.write(`data: ${JSON.stringify({ type: 'error', message: `Failed to launch solver_bin: ${err.message}` })}\n\n`);
      res.end();
    }
  });

  child.on('close', () => {
    if (!cancelled) {
      res.end();
    }
  });
});

app.post('/api/llm-destroy', async (req, res) => {
  try {
    const { routes, instanceName, history, minDestroy = 2, maxDestroy = 3 } = req.body;
    if (!routes || !Array.isArray(routes)) {
      res.status(400).json({ error: 'Routes array is required' });
      return;
    }

    const ollamaConfig = getOllamaConfigFromEnv();

    const routesDescription = routes.map((r: any) => {
      return `Vehicle ${r.vehicleId}: Distance = ${r.distance.toFixed(2)}, Load = ${r.load}, Customers = [${r.customerIds.join(', ')}]`;
    }).join('\n');

    let historyPrompt = "";
    if (history && Array.isArray(history) && history.length > 0) {
      historyPrompt = `\n\nCRITICAL: The following route destructions were already attempted in this run and FAILED to produce a better solution. Do NOT select these combinations of routes or customers again:
` + history.map((h: any, i: number) => {
        return `- Attempt ${i + 1}: Destroyed Vehicle IDs: [${(h.vehicleIds || []).join(', ')}] containing Customer IDs: [${(h.customerIds || []).join(', ')}]`;
      }).join('\n') + `\nPlease select a DIFFERENT set of ${minDestroy} to ${maxDestroy} vehicle routes (by vehicleId) that have not been tried before.`;
    }

    const systemInstruction = `You are an expert operations research assistant. Your task is to analyze vehicle routing (VRPTW) routes and select exactly ${minDestroy} to ${maxDestroy} routes (by vehicleId) that are sub-optimal, overlapping, or inefficient so that we can destroy them and re-solve their customers using Large Neighborhood Search (LNS). Avoid selecting previously attempted routes that failed.`;

    const prompt = `We are solving the Vehicle Routing Problem with Time Windows (VRPTW) for instance "${instanceName || 'Unknown'}".
The current best solution contains the following vehicle routes (trucks):
${routesDescription}
${historyPrompt}

Please analyze these routes. Choose exactly ${minDestroy} to ${maxDestroy} routes to destroy.
Look for:
- Routes with very high distance relative to their load.
- Routes that seem to have very few customers or inefficient paths.
- Geographic overlaps.
Select ${minDestroy} to ${maxDestroy} vehicleId values to destroy.
Return the selected vehicle IDs as a JSON array of integers.`;

    let rawVehicleIds: unknown;
    try {
      rawVehicleIds = await ollamaGenerateJSON(
        ollamaConfig,
        systemInstruction,
        prompt,
        {
          type: 'array',
          items: { type: 'integer' },
          description: `List of vehicle IDs (exactly ${minDestroy} to ${maxDestroy}) to entirely destroy`
        }
      );
    } catch (error: any) {
      console.log('Ollama LLM destroy request failed:', error.message || error);
      // Return 200 OK with empty vehicleIds and error details, so the solver can gracefully fall back to random/heuristic destruction
      res.json({
        vehicleIds: [],
        error: 'Ollama API was unavailable.',
        details: error.message
      });
      return;
    }

    console.log('Ollama LLM destroy response:', rawVehicleIds);
    const validVehicleIds = new Set(routes.map((r: any) => r.vehicleId));
    const vehicleIds = Array.isArray(rawVehicleIds)
      ? rawVehicleIds.filter((id: any) => Number.isInteger(id) && validVehicleIds.has(id))
      : [];

    res.json({ vehicleIds });
  } catch (error: any) {
    console.log('Failed to invoke Ollama for destroy:', error.message || error);
    res.json({
      vehicleIds: [],
      error: 'Failed to invoke Ollama due to an unexpected error.',
      details: error.message
    });
  }
});

async function startServer() {
  // Vite integration
  if (process.env.NODE_ENV !== 'production') {
    const vite = await createViteServer({
      server: { middlewareMode: true },
      appType: 'spa',
    });
    app.use(vite.middlewares);
  } else {
    const distPath = path.join(process.cwd(), 'dist');
    app.use(express.static(distPath));
    app.get('*', (req, res) => {
      res.sendFile(path.join(distPath, 'index.html'));
    });
  }

  app.listen(PORT, '0.0.0.0', () => {
    console.log(`Server running on http://localhost:${PORT}`);
  });
}

startServer();
