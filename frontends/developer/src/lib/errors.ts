export function getErrorMessage(error: unknown, fallback: string): string {
  if (error instanceof Error) {
    const message = error.message.trim();
    if (message.length > 0) {
      return message;
    }
  }

  if (typeof error === "object" && error !== null && "error" in error) {
    const errorValue = error.error;
    if (typeof errorValue === "string") {
      const message = errorValue.trim();
      if (message.length > 0) {
        return message;
      }
    }
  }

  return fallback;
}
