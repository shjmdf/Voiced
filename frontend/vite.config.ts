import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import { readFileSync } from "node:fs";

function requiredValue(environment: Record<string, string>, name: string): string {
  const value = environment[name]?.trim();
  if (!value) {
    throw new Error(`${name} must be set before starting the Vite development server.`);
  }
  return value;
}

function requiredPort(environment: Record<string, string>, name: string): number {
  const value = requiredValue(environment, name);
  const port = Number(value);
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error(`${name} must be an integer from 1 to 65535.`);
  }
  return port;
}

function developmentTLS(environment: Record<string, string>) {
  const keyFile = environment.DEV_HTTPS_KEY_FILE?.trim();
  const certificateFile = environment.DEV_HTTPS_CERT_FILE?.trim();
  if (!keyFile && !certificateFile) return undefined;
  if (!keyFile || !certificateFile) {
    throw new Error("DEV_HTTPS_KEY_FILE and DEV_HTTPS_CERT_FILE must be set together.");
  }

  try {
    return {
      key: readFileSync(keyFile),
      cert: readFileSync(certificateFile),
    };
  } catch (error) {
    const detail = error instanceof Error ? `: ${error.message}` : "";
    throw new Error(`Could not load the development HTTPS certificate${detail}`);
  }
}

export default defineConfig(({ command, mode }) => {
  const environment = loadEnv(mode, process.cwd(), "");
  if (command !== "serve") {
    return { plugins: [react()] };
  }

  const backend = requiredValue(environment, "VITE_BACKEND_URL");
  const https = developmentTLS(environment);
  return {
    plugins: [react()],
    server: {
      host: requiredValue(environment, "VITE_DEV_HOST"),
      port: requiredPort(environment, "VITE_DEV_PORT"),
      strictPort: true,
      https,
      proxy: {
        "/api": { target: backend, changeOrigin: true },
        "/health": { target: backend, changeOrigin: true },
        "/ws": { target: backend, changeOrigin: true, ws: true },
      },
    },
  };
});
