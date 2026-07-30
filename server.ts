import express from 'express';
import path from 'path';
import { createServer as createViteServer } from 'vite';

const app = express();
const PORT = 3000;

// Raw Solomon-format instance text lives in public/data/*.txt, so a plain
// `vite build` copies it into dist/data/*.txt for static hosting (see
// src/parseSolomon.ts, which parses it client-side). No explicit static
// route needed for it here: Vite's own dev middleware (mounted below)
// already serves publicDir contents, and the production branch's
// `express.static(distPath)` covers the built copy the same way.

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
