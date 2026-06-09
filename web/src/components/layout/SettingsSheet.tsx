import { ArrowLeftRight, FileText, KeyRound, Lock, Sliders, User } from "lucide-react";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "../ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../ui/tabs";
import { SsoSettings } from "../sso/SsoSettings";
import { TokensSettings } from "../tokens/TokensSettings";
import { ConsumersSettings } from "../consumers/ConsumersSettings";
import { StreamsSettings } from "../streams/StreamsSettings";
import { TuningSettings } from "../tuning/TuningSettings";
import { ConfigSettings } from "../config/ConfigSettings";

export function SettingsSheet({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent size="xl" className="overflow-hidden">
        <SheetHeader>
          <SheetTitle>Settings</SheetTitle>
          <SheetDescription>
            Configuration surface that doesn&apos;t live in the panel grid — auth, network, history, fine-grained nginx tuning.
          </SheetDescription>
        </SheetHeader>
        <Tabs defaultValue="sso" className="flex-1 min-h-0 overflow-hidden flex flex-col">
          <div className="overflow-x-auto px-6 pt-4 pb-2">
            <TabsList className="inline-flex w-max">
              <TabsTrigger value="sso" className="gap-1.5"><Lock className="h-3.5 w-3.5" /> SSO</TabsTrigger>
              <TabsTrigger value="tokens" className="gap-1.5"><KeyRound className="h-3.5 w-3.5" /> Admin tokens</TabsTrigger>
              <TabsTrigger value="consumers" className="gap-1.5"><User className="h-3.5 w-3.5" /> Consumers</TabsTrigger>
              <TabsTrigger value="streams" className="gap-1.5"><ArrowLeftRight className="h-3.5 w-3.5" /> Streams</TabsTrigger>
              <TabsTrigger value="tuning" className="gap-1.5"><Sliders className="h-3.5 w-3.5" /> Tuning</TabsTrigger>
              <TabsTrigger value="config" className="gap-1.5"><FileText className="h-3.5 w-3.5" /> Config</TabsTrigger>
            </TabsList>
          </div>
          <div className="flex-1 min-h-0 overflow-y-auto px-6 pb-6">
            <TabsContent value="sso"><SsoSettings /></TabsContent>
            <TabsContent value="tokens"><TokensSettings /></TabsContent>
            <TabsContent value="consumers"><ConsumersSettings /></TabsContent>
            <TabsContent value="streams"><StreamsSettings /></TabsContent>
            <TabsContent value="tuning"><TuningSettings /></TabsContent>
            <TabsContent value="config"><ConfigSettings /></TabsContent>
          </div>
        </Tabs>
      </SheetContent>
    </Sheet>
  );
}
