/**
 * Example: An OpenClaw-style plugin that registers tools.
 * This simulates what an npm-published OpenClaw plugin looks like.
 */
module.exports = {
  default: {
    id: "demo",
    name: "Demo OpenClaw Plugin",
    register(api) {
      // Register a weather tool
      api.registerTool({
        name: "get_weather",
        description: "Get current weather for a city",
        parameters: {
          type: "object",
          properties: {
            city: { type: "string", description: "City name" },
          },
          required: ["city"],
        },
        async execute(args) {
          // Simulated weather response
          return `Weather in ${args.city}: 22°C, partly cloudy`;
        },
      });

      // Register a calculator tool
      api.registerTool({
        name: "calculate",
        description: "Evaluate a math expression",
        parameters: {
          type: "object",
          properties: {
            expression: { type: "string", description: "Math expression to evaluate" },
          },
          required: ["expression"],
        },
        async execute(args) {
          try {
            // Simple eval for demo (in real plugin, use a safe parser)
            const result = Function(`"use strict"; return (${args.expression})`)();
            return `${args.expression} = ${result}`;
          } catch (e) {
            return `Error: invalid expression`;
          }
        },
      });
    },
  },
};
