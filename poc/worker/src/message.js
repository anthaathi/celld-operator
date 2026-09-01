export function messageFor(path) {
  return path === "/hello"
    ? "hello from a locally deployed multi-file Worker"
    : "celld local POC is running";
}