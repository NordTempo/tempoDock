import { createFileRoute } from "@tanstack/react-router";
import { CertificatesPage } from "../features/certificates/CertificatesPage";

// The organization's uploaded TLS certificates (ADR 0112).
export const Route = createFileRoute("/_app/$org/settings/certificates")({
  component: CertificatesPage,
});
