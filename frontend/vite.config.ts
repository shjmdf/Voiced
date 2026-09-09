import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";

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

export default defineConfig(({ command, mode }) => {
  const environment = loadEnv(mode, process.cwd(), "");
  if (command !== "serve") {
    return { plugins: [react()] };
  }

  const backend = requiredValue(environment, "VITE_BACKEND_URL");
  return {
    plugins: [react()],
    server: {
      host: requiredValue(environment, "VITE_DEV_HOST"),
      port: requiredPort(environment, "VITE_DEV_PORT"),
      strictPort: true,
      proxy: {
        "/api": { target: backend, changeOrigin: true },
        "/health": { target: backend, changeOrigin: true },
        "/ws": { target: backend, changeOrigin: true, ws: true },
      },
    },
  };
});
