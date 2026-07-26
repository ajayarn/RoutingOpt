import express from 'express';
import path from 'path';
import fs from 'fs';
import { createServer as createViteServer } from 'vite';

const app = express();
const PORT = 3000;

app.use(express.json({ limit: '10mb' }));

// Raw Solomon-format instance text lives in public/data/*.txt (moved there,
// from a former top-level data/, so a plain `vite build` copies it into
// dist/data/*.txt for static hosting - see src/parseSolomon.ts). No explicit
// static route needed for it here: Vite's own dev middleware (mounted below)
// already serves publicDir contents, and the production branch's
// `express.static(distPath)` covers the built copy the same way.

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

// 2. API: Get list of instances (unused by the frontend now - see
// src/App.tsx, which parses public/data/*.txt client-side instead - kept
// working here for anyone hitting it directly)
app.get('/api/instances', (req, res) => {
  const dirPath = path.join(process.cwd(), 'public', 'data');
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

// 3. API: Get details of a specific instance (unused by the frontend now,
// same as above)
app.get('/api/instances/:id', (req, res) => {
  const instanceId = req.params.id;
  const filePath = path.join(process.cwd(), 'public', 'data', `${instanceId}.txt`);

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

// 4. API: Upload raw custom Solomon text content (unused by the frontend
// now - src/App.tsx's handleUpload parses+holds uploads client-side/in-memory
// instead, since a static deploy has nowhere to persist a file to; kept
// working here for anyone hitting it directly against a running dev server)
app.post('/api/upload', (req, res) => {
  const { name, content } = req.body;
  if (!name || !content) {
    return res.status(400).json({ error: 'Name and content are required' });
  }

  try {
    const safeName = name.replace(/[^a-zA-Z0-9_\-]/g, '').toLowerCase();
    const filePath = path.join(process.cwd(), 'public', 'data', `${safeName}.txt`);
    fs.mkdirSync(path.join(process.cwd(), 'public', 'data'), { recursive: true });
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
