import { useState } from "react";
import { LogIn } from "lucide-react";
import { useAuth } from "../../auth/auth-context";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "../ui/dialog";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Label } from "../ui/label";

export function SignInDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const { signIn } = useAuth();
  const [value, setValue] = useState("");

  function submit(e: React.FormEvent) {
    e.preventDefault();
    const v = value.trim();
    if (!v) return;
    signIn(v);
    setValue("");
    onOpenChange(false);
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="sm">
        <DialogHeader>
          <DialogTitle>Sign in</DialogTitle>
          <DialogDescription>
            Paste your <code className="font-mono text-xs">Authorization: Bearer &lt;token&gt;</code> value
            from <code className="font-mono text-xs">/etc/apigw/config.yaml</code>. The token stays in
            this browser&apos;s localStorage; sign out clears it.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="signin-token">Bearer token</Label>
            <Input
              id="signin-token"
              type="password"
              autoComplete="off"
              placeholder="e.g. my-super-secret-32-byte-token"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              autoFocus
            />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
            <Button type="submit" className="gap-1.5"><LogIn className="h-3.5 w-3.5" /> Sign in</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
