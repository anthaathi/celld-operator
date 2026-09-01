import { messageFor } from "./message.js";

export default {
  async fetch(request) {
    const url = new URL(request.url);
    return Response.json({
      message: messageFor(url.pathname),
      runtime: "celld",
      path: url.pathname,
    });
  },
};