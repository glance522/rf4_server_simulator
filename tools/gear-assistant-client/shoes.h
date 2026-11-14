// Optional running speed change, controlled by committed assistant replies.
namespace gear_assistant {
using namespace gear_bridge;
using Constructor = void (*)(void*, int, int, long long, void*, const void*);
using Receive = int (*)(void*, void*, bool, const void*);
using Move = void (*)(void*, float, const void*);
using GCNew = uint32_t (*)(void*, bool);
using GCTarget = void* (*)(uint32_t);
using GCFree = void (*)(uint32_t);
using StringNew = void* (*)(const char*);
using ClassMethods = const void* (*)(void*, void**);
using MethodName = const char* (*)(const void*);
using ParamCount = uint32_t (*)(const void*);
using MethodParam = const void* (*)(const void*, uint32_t);
using TypeName = char* (*)(const void*);
using Free = void (*)(void*);

static Constructor originalConstructor{};
static Receive originalReceive{};
static Move originalMove{};
static GCNew gcNew{};
static GCTarget gcTarget{};
static GCFree gcFree{};
static StringNew stringNew{};
static void* messageClass{};
static const void* sendText{};
static size_t selfOffset{}, senderOffset{}, textOffset{}, guidOffset{}, speedOffset{};
static std::atomic<bool> shoes{false};
static int account{};
static uint64_t lastMail{};
static uint32_t pendingContact{};
static ULONGLONG nextSyncAt{};
static int syncAttempts{};
// Copy only explicitly checked, position-independent whole instructions.
template<class Function>
static void* hook(const void* method, const BYTE* expected, size_t size, const void* replacement, Function& original) {
    auto target = method ? *reinterpret_cast<BYTE* const*>(method) : nullptr;
    if (!target || size < 5 || size > 16 || memcmp(target, expected, size)) return nullptr;
    SYSTEM_INFO info{}; GetSystemInfo(&info);
    const uintptr_t step = info.dwAllocationGranularity;
    const uintptr_t base = reinterpret_cast<uintptr_t>(target) & ~(step - 1);
    BYTE* bridge = nullptr;
    for (uintptr_t delta = step; delta < 0x7fff0000 && !bridge; delta += step) {
        for (int direction : {1, -1}) {
            const auto at = direction > 0 ? base + delta : base - delta;
            if (at < step || at > reinterpret_cast<uintptr_t>(info.lpMaximumApplicationAddress)) continue;
            MEMORY_BASIC_INFORMATION region{};
            if (!VirtualQuery(reinterpret_cast<void*>(at), &region, sizeof(region)) || region.State != MEM_FREE) continue;
            bridge = static_cast<BYTE*>(VirtualAlloc(reinterpret_cast<void*>(at), 64, MEM_RESERVE | MEM_COMMIT, PAGE_READWRITE));
            if (bridge) break;
        }
    }
    if (!bridge) return nullptr;
    memcpy(bridge, target, size);
    absoluteJump(bridge + size, target + size);
    absoluteJump(bridge + 32, replacement);
    DWORD old{};
    if (!VirtualProtect(bridge, 64, PAGE_EXECUTE_READ, &old)) { VirtualFree(bridge, 0, MEM_RELEASE); return nullptr; }
    FlushInstructionCache(GetCurrentProcess(), bridge, 64);
    const auto relative = reinterpret_cast<intptr_t>(bridge + 32) - reinterpret_cast<intptr_t>(target + 5);
    const auto displacement = static_cast<int32_t>(relative);
    if (relative != displacement || !VirtualProtect(target, size, PAGE_EXECUTE_READWRITE, &old)) {
        VirtualFree(bridge, 0, MEM_RELEASE); return nullptr;
    }
    BYTE patch[16]; memset(patch, 0x90, sizeof(patch)); patch[0] = 0xe9;
    memcpy(patch + 1, &displacement, 4);
    original = reinterpret_cast<Function>(bridge);
    memcpy(target, patch, size);
    FlushInstructionCache(GetCurrentProcess(), target, size);
    DWORD unused{}; VirtualProtect(target, size, old, &unused);
    return bridge;
}

static bool parseSetting(const std::wstring& text, bool& fast, bool& quiet) {
    quiet = text.rfind(L"钓具设置已同步。\n", 0) == 0;
    if (!quiet) {
        // Read the existing feature acknowledgement without changing its text.
        for (const auto prefix : {L"【五速鞋】已开启", L"【五速鞋】已经开启"})
            if (text.rfind(prefix, 0) == 0) { fast = true; return true; }
        for (const auto prefix : {L"【五速鞋】已关闭", L"【五速鞋】已经关闭"})
            if (text.rfind(prefix, 0) == 0) { fast = false; return true; }
        return false;
    }
    const auto p = text.rfind(L"\n五速鞋：");
    if (p == std::wstring::npos) return false;
    const auto tail = text.substr(p);
    if (tail == L"\n五速鞋：开启") { fast = true; return true; }
    if (tail == L"\n五速鞋：关闭") { fast = false; return true; }
    return false;
}

static int received(void* conversation, void* meta, bool notify, const void* method) {
    if (!conversation || !meta || *reinterpret_cast<void**>(meta) != messageClass)
        return originalReceive(conversation, meta, notify, method);
    const auto uid = *reinterpret_cast<int*>(reinterpret_cast<BYTE*>(conversation) + selfOffset);
    const auto sender = *reinterpret_cast<int*>(reinterpret_cast<BYTE*>(meta) + senderOffset);
    if (uid != account || sender != 2147483646)
        return originalReceive(conversation, meta, notify, method);
    auto text = *reinterpret_cast<void**>(reinterpret_cast<BYTE*>(meta) + textOffset);
    const auto content = managedText(text);
    bool fast = false;
    bool quiet = false;
    const bool snapshot = parseSetting(content, fast, quiet);
    const auto id = reinterpret_cast<BYTE*>(meta) + guidOffset;
    // Server mail GUIDs encode the positive row ID in the final eight bytes.
    uint64_t number = 0;
    for (int i = 0; i < 8; ++i) if (id[i])
        return originalReceive(conversation, meta, notify, method);
    for (int i = 8; i < 16; ++i) number = (number << 8) | id[i];
    if (number > lastMail && snapshot) {
        lastMail = number; shoes = fast;
        log("gear-assistant settings-confirmed shoes=" + std::to_string(fast));
    }
    // The native result counts newly unread items; a consumed sync adds none.
    // Keep it out of the conversation UI, including replayed sync snapshots.
    if (number && snapshot && quiet) return 0;
    return originalReceive(conversation, meta, notify, method);
}

// Contact construction can happen on a network thread without a Windows
// message loop. Send from Unity's update hooks rather than a thread timer.
static void syncSettings() {
    if (!pendingContact) return;
    const auto now = GetTickCount64();
    if (now < nextSyncAt) return;
    nextSyncAt = now + 500;
    auto contact = pendingContact ? gcTarget(pendingContact) : nullptr;
    void* result = nullptr;
    auto text = stringNew("同步设置");
    void* args[] = {text};
    const bool sent = contact && invoke(sendText, contact, args, "gear-sync", &result) && result;
    if (!sent && ++syncAttempts < 20) return;
    if (pendingContact) gcFree(pendingContact);
    pendingContact = 0;
    log(sent ? "gear-assistant sync-requested" : "gear-assistant sync-failed");
}

static void constructed(void* object, int self, int other, long long latest, void* model, const void* method) {
    originalConstructor(object, self, other, latest, model, method);
    if (other != 2147483646 || self <= 0) return;
    if (pendingContact) gcFree(pendingContact);
    account = self; lastMail = 0; shoes = false;
    pendingContact = gcNew(object, false); syncAttempts = 0;
    nextSyncAt = GetTickCount64() + 500;
}

static void moving(void* object, float dt, const void* method) {
    syncSettings();
    auto& speed = *reinterpret_cast<float*>(reinterpret_cast<BYTE*>(object) + speedOffset);
    const float baseline = speed;
    const bool enabled = shoes.load() && baseline > 0 && baseline < 100;
    if (enabled) speed = baseline * 3.0f;
    originalMove(object, dt, method);
    if (enabled) speed = baseline;
}

static const void* unaryMethod(HMODULE module, void* cls, const char* wantedName, const char* wantedType) {
    const auto methods = reinterpret_cast<ClassMethods>(realGetProc(module, "il2cpp_class_get_methods"));
    const auto name = reinterpret_cast<MethodName>(realGetProc(module, "il2cpp_method_get_name"));
    const auto count = reinterpret_cast<ParamCount>(realGetProc(module, "il2cpp_method_get_param_count"));
    const auto param = reinterpret_cast<MethodParam>(realGetProc(module, "il2cpp_method_get_param"));
    const auto typeName = reinterpret_cast<TypeName>(realGetProc(module, "il2cpp_type_get_name"));
    const auto free = reinterpret_cast<Free>(realGetProc(module, "il2cpp_free"));
    if (!methods || !name || !count || !param || !typeName || !free) return nullptr;
    void* it = nullptr;
    while (auto method = methods(cls, &it)) {
        if (strcmp(name(method), wantedName) || count(method) != 1) continue;
        auto text = typeName(param(method, 0));
        const bool match = text && !strcmp(text, wantedType);
        if (text) free(text);
        if (match) return method;
    }
    return nullptr;
}

static bool start(HMODULE module) {
    if (!resolve(module)) return false;
    gcNew = reinterpret_cast<GCNew>(realGetProc(module, "il2cpp_gchandle_new"));
    gcTarget = reinterpret_cast<GCTarget>(realGetProc(module, "il2cpp_gchandle_get_target"));
    gcFree = reinterpret_cast<GCFree>(realGetProc(module, "il2cpp_gchandle_free"));
    stringNew = reinterpret_cast<StringNew>(realGetProc(module, "il2cpp_string_new"));
    if (!gcNew || !gcTarget || !gcFree || !stringNew) return false;
    void* domain = nullptr;
    void* game = nullptr;
    // The DLL can be mapped before IL2CPP initializes its GC. A non-null
    // domain alone is insufficient; wait for the core image before attaching.
    log("gear-assistant waiting-runtime");
    for (int attempt = 0; attempt < 3000 && !stopping; ++attempt) {
        if (state.api.getCorlib()) {
            domain = state.api.domainGet();
            if (domain) break;
        }
        Sleep(100);
    }
    if (!domain || stopping) { log("gear-assistant runtime-not-ready"); return false; }
    auto thread = state.api.attach(domain);
    if (!thread) return false;
    log("gear-assistant worker-attached");
    // All metadata access, including assembly enumeration, belongs after
    // thread registration. Detach only this worker when setup is finished.
    for (int attempt = 0; attempt < 3000 && !stopping; ++attempt) {
        game = findImage(domain, "Assembly-CSharp.dll");
        if (game) break;
        Sleep(100);
    }
    if (!game || stopping) {
        state.api.detach(thread);
        log("gear-assistant assembly-not-ready");
        return false;
    }
    auto fps = state.api.classFromName(game, "RF4.Client.FishingScene", "FisherFPSNew");
    auto conversation = state.api.classFromName(game, "", "kjbnbfcfikl");
    messageClass = state.api.classFromName(game, "RF4.API.Mailbox", "MailboxMessageMeta");
    const auto ctor = conversation ? state.api.methodFromName(conversation, ".ctor", 4) : nullptr;
    const auto receive = conversation ? state.api.methodFromName(conversation, "jdoaiknmcnb", 2) : nullptr;
    const auto move = fps ? state.api.methodFromName(fps, "cncmhpjmcao", 1) : nullptr;
    sendText = unaryMethod(module, conversation, "piaopgakhag", "System.String");
    bool ok = fps && conversation && messageClass && ctor && receive && move && sendText;
    ok = ok && fieldOffsetInHierarchy(conversation, "jdkmkhobpap", selfOffset)
        && fieldOffsetInHierarchy(messageClass, "sender", senderOffset)
        && fieldOffsetInHierarchy(messageClass, "text", textOffset)
        && fieldOffsetInHierarchy(messageClass, "id", guidOffset)
        && fieldOffsetInHierarchy(fps, "_runSpeed", speedOffset);
    const BYTE ctorBytes[] = {0x48,0x89,0x5c,0x24,0x08};
    const BYTE recvBytes[] = {0x48,0x89,0x5c,0x24,0x18};
    const BYTE moveBytes[] = {0x48,0x8b,0xc4,0x48,0x89,0x58,0x10};
    auto matches = [](const void* method, const BYTE* bytes, size_t size) {
        auto code = method ? *reinterpret_cast<BYTE* const*>(method) : nullptr;
        return code && !memcmp(code, bytes, size);
    };
    ok = ok && matches(ctor,ctorBytes,sizeof(ctorBytes)) && matches(receive,recvBytes,sizeof(recvBytes))
        && matches(move,moveBytes,sizeof(moveBytes));
    if (ok) {
        hook(receive,recvBytes,sizeof(recvBytes),reinterpret_cast<void*>(received),originalReceive);
        hook(move,moveBytes,sizeof(moveBytes),reinterpret_cast<void*>(moving),originalMove);
        // Settings stay disabled unless all hooks are ready.
        if (originalReceive && originalMove)
            hook(ctor,ctorBytes,sizeof(ctorBytes),reinterpret_cast<void*>(constructed),originalConstructor);
        ok = originalConstructor != nullptr;
    }
    state.api.detach(thread);
    log(ok ? "gear-assistant bridge-ready" : "gear-assistant metadata-or-hook-mismatch");
    return ok;
}
} // namespace gear_assistant
