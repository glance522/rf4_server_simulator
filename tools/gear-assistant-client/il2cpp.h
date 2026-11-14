// Shared IL2CPP utilities for the optional gear module.
namespace gear_bridge {
using DomainGet = void* (*)();
using GetCorlib = void* (*)();
using DomainAssemblies = const void** (*)(void*, size_t*);
using ThreadAttach = void* (*)(void*);
using ThreadDetach = void (*)(void*);
using AssemblyImage = void* (*)(const void*);
using ImageName = const char* (*)(void*);
using ClassFromName = void* (*)(void*, const char*, const char*);
using ClassParent = void* (*)(void*);
using FieldFromName = void* (*)(void*, const char*);
using FieldOffset = size_t (*)(void*);
using MethodFromName = const void* (*)(void*, const char*, int);
using RuntimeInvoke = void* (*)(const void*, void*, void**, void**);
using StringLength = int (*)(void*);
using StringChars = const wchar_t* (*)(void*);
using FormatException = void (*)(void*, char*, int);

struct Api {
    DomainGet domainGet{};
    GetCorlib getCorlib{};
    DomainAssemblies assemblies{};
    ThreadAttach attach{};
    ThreadDetach detach{};
    AssemblyImage assemblyImage{};
    ImageName imageName{};
    ClassFromName classFromName{};
    ClassParent classParent{};
    FieldFromName fieldFromName{};
    FieldOffset fieldOffset{};
    MethodFromName methodFromName{};
    RuntimeInvoke runtimeInvoke{};
    StringLength stringLength{};
    StringChars stringChars{};
    FormatException formatException{};
};

struct State { Api api{}; };
static State state;
static void* findImage(void* domain, const char* wanted) {
    size_t count = 0;
    const auto list = state.api.assemblies(domain, &count);
    for (size_t i = 0; list && i < count; ++i) {
        auto image = state.api.assemblyImage(list[i]);
        const char* name = image ? state.api.imageName(image) : nullptr;
        if (name && !strcmp(name, wanted)) return image;
    }
    return nullptr;
}

static bool fieldOffsetInHierarchy(void* cls, const char* name, size_t& offset) {
    for (void* current = cls; current; current = state.api.classParent(current)) {
        if (auto field = state.api.fieldFromName(current, name)) {
            offset = state.api.fieldOffset(field);
            return true;
        }
    }
    return false;
}

static bool invoke(const void* method, void* instance, void** args, const char* label, void** output = nullptr) {
    if (!method) return false;
    void* exception = nullptr;
    void* result = state.api.runtimeInvoke(method, instance, args, &exception);
    if (exception) {
        char detail[512]{};
        if (state.api.formatException) state.api.formatException(exception, detail, sizeof(detail));
        log(std::string("gear-assistant invoke-failed ") + label + " " + detail);
        return false;
    }
    if (output) *output = result;
    return true;
}

static std::wstring managedText(void* value) {
    if (!value) return {};
    const int length = state.api.stringLength(value);
    const wchar_t* chars = state.api.stringChars(value);
    return length > 0 && chars ? std::wstring(chars, chars + length) : std::wstring();
}

static void absoluteJump(BYTE* at, const void* destination) {
    const BYTE prefix[] = {0xff, 0x25, 0, 0, 0, 0};
    memcpy(at, prefix, sizeof(prefix));
    memcpy(at + sizeof(prefix), &destination, sizeof(destination));
}

static bool resolve(HMODULE module) {
    auto proc = [&](const char* name) { return realGetProc(module, name); };
    state.api.domainGet = reinterpret_cast<DomainGet>(proc("il2cpp_domain_get"));
    state.api.getCorlib = reinterpret_cast<GetCorlib>(proc("il2cpp_get_corlib"));
    state.api.assemblies = reinterpret_cast<DomainAssemblies>(proc("il2cpp_domain_get_assemblies"));
    state.api.attach = reinterpret_cast<ThreadAttach>(proc("il2cpp_thread_attach"));
    state.api.detach = reinterpret_cast<ThreadDetach>(proc("il2cpp_thread_detach"));
    state.api.assemblyImage = reinterpret_cast<AssemblyImage>(proc("il2cpp_assembly_get_image"));
    state.api.imageName = reinterpret_cast<ImageName>(proc("il2cpp_image_get_name"));
    state.api.classFromName = reinterpret_cast<ClassFromName>(proc("il2cpp_class_from_name"));
    state.api.classParent = reinterpret_cast<ClassParent>(proc("il2cpp_class_get_parent"));
    state.api.fieldFromName = reinterpret_cast<FieldFromName>(proc("il2cpp_class_get_field_from_name"));
    state.api.fieldOffset = reinterpret_cast<FieldOffset>(proc("il2cpp_field_get_offset"));
    state.api.methodFromName = reinterpret_cast<MethodFromName>(proc("il2cpp_class_get_method_from_name"));
    state.api.runtimeInvoke = reinterpret_cast<RuntimeInvoke>(proc("il2cpp_runtime_invoke"));
    state.api.stringLength = reinterpret_cast<StringLength>(proc("il2cpp_string_length"));
    state.api.stringChars = reinterpret_cast<StringChars>(proc("il2cpp_string_chars"));
    state.api.formatException = reinterpret_cast<FormatException>(proc("il2cpp_format_exception"));
    if (!state.api.domainGet || !state.api.getCorlib || !state.api.assemblies || !state.api.attach || !state.api.detach ||
        !state.api.assemblyImage || !state.api.imageName || !state.api.classFromName || !state.api.classParent ||
        !state.api.fieldFromName || !state.api.fieldOffset || !state.api.methodFromName ||
        !state.api.runtimeInvoke || !state.api.stringLength || !state.api.stringChars) {
        log("gear-assistant missing-il2cpp-api");
        return false;
    }
    return true;
}
} // namespace gear_bridge
