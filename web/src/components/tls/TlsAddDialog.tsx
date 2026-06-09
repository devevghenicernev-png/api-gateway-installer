import { useState } from "react";
import { ShieldCheck } from "lucide-react";
import { useEnableTLS } from "../../hooks/useAdminQueries";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "../ui/dialog";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";

type Issuer = "letsencrypt" | "duckdns" | "selfsigned";

export function TlsAddDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const enableTLS = useEnableTLS();
  const [issuer, setIssuer] = useState<Issuer>("letsencrypt");
  const [domain, setDomain] = useState("");
  const [email, setEmail] = useState("");
  const [token, setToken] = useState("");

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!domain) return;
    try {
      await enableTLS.mutateAsync({ domain, issuer, email, token: issuer === "duckdns" ? token : undefined });
      setDomain(""); setToken(""); setEmail("");
      onOpenChange(false);
    } catch { /* toast'd */ }
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2"><ShieldCheck className="h-4 w-4" /> Add TLS domain</DialogTitle>
          <DialogDescription>
            apigw triggers an ACME challenge (HTTP-01 for Let&apos;s Encrypt, DNS-01 for DuckDNS) and wires the cert into the nginx listener.
            Watch Live logs for progress; first issuance takes ~30s.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="tls-issuer">Issuer</Label>
            <Select value={issuer} onValueChange={(v) => setIssuer(v as Issuer)}>
              <SelectTrigger id="tls-issuer"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="letsencrypt">Let&apos;s Encrypt (public domain, HTTP-01)</SelectItem>
                <SelectItem value="duckdns">DuckDNS (DNS-01, free *.duckdns.org)</SelectItem>
                <SelectItem value="selfsigned">Self-signed (dev / staging)</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="tls-domain">Domain</Label>
            <Input id="tls-domain" value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="api.example.com" required autoFocus />
          </div>
          {issuer !== "selfsigned" && (
            <div className="space-y-1.5">
              <Label htmlFor="tls-email">Notification email</Label>
              <Input id="tls-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@example.com" required />
              <p className="text-[11px] text-muted-foreground">LE sends expiry warnings here when renewal fails.</p>
            </div>
          )}
          {issuer === "duckdns" && (
            <div className="space-y-1.5">
              <Label htmlFor="tls-token">DuckDNS API token</Label>
              <Input id="tls-token" value={token} onChange={(e) => setToken(e.target.value)} placeholder="xxxx-xxxx-xxxx-xxxx-xxxx" required />
              <p className="text-[11px] text-muted-foreground">Get yours at https://www.duckdns.org/</p>
            </div>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
            <Button type="submit" disabled={enableTLS.isPending || !domain}>
              {enableTLS.isPending ? "Requesting…" : "Issue certificate"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
