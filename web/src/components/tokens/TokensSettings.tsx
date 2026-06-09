import { useState } from "react";
import { KeyRound, Plus, Trash2 } from "lucide-react";
import { useAddAdminToken, useAdminTokens, useRemoveAdminToken } from "../../hooks/useAdminQueries";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { Skeleton } from "../ui/skeleton";

export function TokensSettings() {
  const { data, isLoading } = useAdminTokens();
  const add = useAddAdminToken();
  const remove = useRemoveAdminToken();
  const [name, setName] = useState("");
  const [user, setUser] = useState("");
  const [token, setToken] = useState("");

  return (
    <div className="space-y-4">
      <form
        onSubmit={(e) => { e.preventDefault(); if (!name || !user || !token) return; add.mutate({ Name: name, User: user, Token: token }); setName(""); setUser(""); setToken(""); }}
        className="grid gap-3 sm:grid-cols-3 rounded-md border border-border bg-card p-3"
      >
        <div className="space-y-1"><Label>Name</Label><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="ci-runner" /></div>
        <div className="space-y-1"><Label>User</Label><Input value={user} onChange={(e) => setUser(e.target.value)} placeholder="ci@example.com" /></div>
        <div className="space-y-1"><Label>Token</Label><Input type="password" value={token} onChange={(e) => setToken(e.target.value)} placeholder="32+ random bytes" /></div>
        <div className="sm:col-span-3 flex justify-end">
          <Button type="submit" size="sm" className="gap-1.5" disabled={add.isPending || !name || !user || !token}>
            <Plus className="h-3.5 w-3.5" /> Add token
          </Button>
        </div>
      </form>
      <div>
        {isLoading ? <Skeleton className="h-12 w-full" /> : !data || data.length === 0 ? (
          <div className="rounded-md border border-dashed p-6 text-center text-sm text-muted-foreground">No admin tokens issued. The owner bootstrap token (printed during install) still works until removed here.</div>
        ) : (
          <ul className="space-y-1.5">
            {data.map((t) => (
              <li key={t.Name} className="flex items-center gap-2 rounded-md border border-border bg-card px-3 py-2">
                <KeyRound className="h-4 w-4 text-muted-foreground" />
                <span className="font-semibold text-sm">{t.Name}</span>
                <span className="text-xs text-muted-foreground font-mono">{t.User}</span>
                <Button variant="ghost" size="icon" className="ml-auto text-destructive hover:bg-destructive/10" onClick={() => remove.mutate(t.Name)} aria-label="Remove">
                  <Trash2 className="h-4 w-4" />
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
