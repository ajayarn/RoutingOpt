export interface OllamaConfig {
  baseUrl: string;
  model: string;
}

export function getOllamaConfigFromEnv(): OllamaConfig {
  return {
    baseUrl: process.env.OLLAMA_BASE_URL || 'http://localhost:11434',
    model: process.env.OLLAMA_MODEL || 'gemma4:12b',
  };
}

export async function ollamaGenerateJSON(
  config: OllamaConfig,
  system: string,
  prompt: string,
  schema: object
): Promise<any> {
  const res = await fetch(`${config.baseUrl}/api/generate`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      model: config.model,
      system,
      prompt,
      format: schema,
      stream: false,
    }),
  });

  if (!res.ok) {
    throw new Error(`Ollama request failed: ${res.status} ${await res.text()}`);
  }

  const data = await res.json();
  return JSON.parse(data.response);
}
