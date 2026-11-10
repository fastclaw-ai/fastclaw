"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";

// Console → Connectors moved under Console → Tools; keep old links working.
export default function ConsoleConnectorsPage() {
  const router = useRouter();
  useEffect(() => {
    router.replace("/console/tools/");
  }, [router]);
  return null;
}
