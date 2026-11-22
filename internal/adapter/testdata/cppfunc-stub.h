// cppfunc-stub.h - minimal std::string stand-in so the fixture parses
// without the real (multi-megabyte) standard library headers. The
// function defined here also pins the file filter: header-declared
// functions are never tasks even when non-static.
#pragma once
namespace std {
struct string {
    string();
    string(const char*);
};
}
void declared_in_header(std::string s = "from header") {}
