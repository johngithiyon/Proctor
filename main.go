package main

import (
    "fmt"
    "html/template"
    "io/ioutil"
    "net/http"
    "net/url"
    "os"
    "strings"
    "sync"
    "encoding/base64"
    "path/filepath"
)

var templates = template.Must(template.ParseGlob("templates/*.html"))

// --- User and Data Structures ---
var studentUser = map[string]string{
    "student1": "1234",
}
var adminUser = map[string]string{
    "admin": "admin123",
}
var exams = []string{
    "Math Exam - Grade 10",
    "Science Exam - Grade 10",
}
type Result struct {
    Username string
    Score    int
}
type Violation struct {
    Username string
    Count    int
}
var results []Result
var violations []Violation
var mu sync.Mutex

// Store reference faces for each user
var userReferenceFaces = make(map[string]string)

func main() {
    os.MkdirAll("captured_images", os.ModePerm)
    os.MkdirAll("reference_faces", os.ModePerm)
    http.HandleFunc("/", loginPage)
    http.HandleFunc("/login", loginHandler)
    http.HandleFunc("/exam", examPage)
    http.HandleFunc("/proctor", proctorPage)
    http.HandleFunc("/capture", captureHandler)
    http.HandleFunc("/submit", submitHandler)
    http.HandleFunc("/score", scorePage)
    http.HandleFunc("/admin", adminPage)
    http.HandleFunc("/fullscreen-violation", fullscreenViolationHandler)
    http.HandleFunc("/tab-change-violation", tabChangeViolationHandler)
    http.HandleFunc("/window-change-violation", windowChangeViolationHandler)
    fmt.Println("Server running on http://localhost:8080")
    http.ListenAndServe(":8080", nil)
}

// --- Page Renderers ---
func loginPage(w http.ResponseWriter, r *http.Request) {
    templates.ExecuteTemplate(w, "login.html", nil)
}

func examPage(w http.ResponseWriter, r *http.Request) {
    username := r.URL.Query().Get("user")
    data := struct {
        Username string
        Exams    []string
    }{username, exams}
    templates.ExecuteTemplate(w, "exam.html", data)
}

func proctorPage(w http.ResponseWriter, r *http.Request) {
    username := r.URL.Query().Get("user")
    templates.ExecuteTemplate(w, "proctor.html", map[string]string{"Username": username})
}

func scorePage(w http.ResponseWriter, r *http.Request) {
    username := r.URL.Query().Get("user")
    var studentScore int
    mu.Lock()
    for _, res := range results {
        if res.Username == username {
            studentScore = res.Score
            break
        }
    }
    mu.Unlock()
    templates.ExecuteTemplate(w, "score.html", struct {
        Username string
        Score    int
    }{username, studentScore})
}

func adminPage(w http.ResponseWriter, r *http.Request) {
    mu.Lock()
    defer mu.Unlock()
    
    type AdminData struct {
        Results    []Result
        Violations []Violation
    }
    
    data := AdminData{
        Results:    results,
        Violations: violations,
    }
    
    templates.ExecuteTemplate(w, "admin.html", data)
}

// --- Handlers ---
func loginHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        http.Redirect(w, r, "/", http.StatusSeeOther)
        return
    }
    
    username := r.FormValue("username")
    password := r.FormValue("password")
    role := r.FormValue("role")
    faceImage := r.FormValue("face_image")

    // Validate credentials
    if role == "student" {
        if pass, ok := studentUser[username]; !ok || pass != password {
            templates.ExecuteTemplate(w, "login.html", "Invalid credentials!")
            return
        }
    } else if role == "admin" {
        if pass, ok := adminUser[username]; !ok || pass != password {
            templates.ExecuteTemplate(w, "login.html", "Invalid credentials!")
            return
        }
        // Admin doesn't need face verification
        http.Redirect(w, r, "/admin", http.StatusSeeOther)
        return
    }

    // Save face image for student
    if faceImage != "" {
        // Decode base64 image
        parts := strings.Split(faceImage, ",")
        if len(parts) != 2 {
            templates.ExecuteTemplate(w, "login.html", "Invalid face image format!")
            return
        }
        
        decoded, err := base64.StdEncoding.DecodeString(parts[1])
        if err != nil {
            templates.ExecuteTemplate(w, "login.html", "Failed to process face image!")
            return
        }
        
        // Save reference face
        referenceFacePath := filepath.Join("reference_faces", username+".jpg")
        err = ioutil.WriteFile(referenceFacePath, decoded, 0644)
        if err != nil {
            templates.ExecuteTemplate(w, "login.html", "Failed to save face image!")
            return
        }
        
        // Store reference face path for this user
        mu.Lock()
        userReferenceFaces[username] = referenceFacePath
        mu.Unlock()
        
        // Redirect to exam page
        http.Redirect(w, r, "/exam?user="+username, http.StatusSeeOther)
    } else {
        templates.ExecuteTemplate(w, "login.html", "Please capture your face photo!")
    }
}

// Forward captured data to Python OpenCV service
func captureHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        w.WriteHeader(http.StatusBadRequest)
        return
    }

    imgData := r.FormValue("image")
    username := r.FormValue("username")
    noiseViolation := r.FormValue("noise_violation")

    // Get reference face path for this user
    mu.Lock()
    referenceFacePath, exists := userReferenceFaces[username]
    mu.Unlock()
    
    if !exists {
        w.WriteHeader(http.StatusInternalServerError)
        w.Write([]byte("ERROR: No reference face found for user"))
        return
    }

    resp, err := http.PostForm("http://localhost:5000/capture", url.Values{
        "image":           {imgData},
        "username":        {username},
        "noise_violation": {noiseViolation},
        "reference_face":  {referenceFacePath},
    })
    if err != nil {
        w.WriteHeader(http.StatusInternalServerError)
        w.Write([]byte("ERROR"))
        return
    }
    defer resp.Body.Close()
    body, _ := ioutil.ReadAll(resp.Body)
    
    responseStr := string(body)
    
    // Handle specific termination messages
    if responseStr == "FACE_MISMATCH" {
        w.Write([]byte("FACE_MISMATCH"))
        return
    }
    
    // Handle multiple faces detection
    if responseStr == "MULTIPLE_FACES" {
        w.Write([]byte("MULTIPLE_FACES"))
        return
    }

    // Handle violations, including the new PROHIBITED_ITEM type
    // The condition now checks for "_VIOLATION" (e.g., GAZE_VIOLATION) OR "PROHIBITED_ITEM"
    if strings.Contains(responseStr, "_VIOLATION") || strings.Contains(responseStr, "PROHIBITED_ITEM") {
        mu.Lock()
        found := false
        for i, v := range violations {
            if v.Username == username {
                violations[i].Count++
                found = true
                
                if violations[i].Count >= 10 {
                    mu.Unlock()
                    w.Write([]byte("MAX_VIOLATIONS"))
                    return
                }
                
                // Send a more specific violation message to the frontend
                // The responseStr could be "GAZE_VIOLATION" or "PROHIBITED_ITEM:MOBILE_PHONE"
                w.Write([]byte(fmt.Sprintf("VIOLATION:%s:%d", responseStr, violations[i].Count)))
                mu.Unlock()
                return
            }
        }
        
        if !found {
            violations = append(violations, Violation{Username: username, Count: 1})
            w.Write([]byte(fmt.Sprintf("VIOLATION:%s:1", responseStr)))
        }
        mu.Unlock()
        return
    }
    
    // Pass through any other response (like "OK")
    w.Write(body)
}

// Handle fullscreen violation
func fullscreenViolationHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        w.WriteHeader(http.StatusBadRequest)
        return
    }

    username := r.FormValue("username")
    
    mu.Lock()
    found := false
    for i, v := range violations {
        if v.Username == username {
            violations[i].Count++
            found = true
            
            if violations[i].Count >= 10 {
                mu.Unlock()
                w.Write([]byte("MAX_VIOLATIONS"))
                return
            }
            
            w.Write([]byte(fmt.Sprintf("VIOLATION:FULLSCREEN_VIOLATION:%d", violations[i].Count)))
            mu.Unlock()
            return
        }
    }
    
    if !found {
        violations = append(violations, Violation{Username: username, Count: 1})
        w.Write([]byte(fmt.Sprintf("VIOLATION:FULLSCREEN_VIOLATION:1")))
    }
    mu.Unlock()
}

// Handle tab change violation
func tabChangeViolationHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        w.WriteHeader(http.StatusBadRequest)
        return
    }

    username := r.FormValue("username")
    
    mu.Lock()
    found := false
    for i, v := range violations {
        if v.Username == username {
            violations[i].Count++
            found = true
            
            if violations[i].Count >= 10 {
                mu.Unlock()
                w.Write([]byte("MAX_VIOLATIONS"))
                return
            }
            
            w.Write([]byte(fmt.Sprintf("VIOLATION:TAB_CHANGE_VIOLATION:%d", violations[i].Count)))
            mu.Unlock()
            return
        }
    }
    
    if !found {
        violations = append(violations, Violation{Username: username, Count: 1})
        w.Write([]byte(fmt.Sprintf("VIOLATION:TAB_CHANGE_VIOLATION:1")))
    }
    mu.Unlock()
}

// Handle window change violation
func windowChangeViolationHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        w.WriteHeader(http.StatusBadRequest)
        return
    }

    username := r.FormValue("username")
    
    mu.Lock()
    found := false
    for i, v := range violations {
        if v.Username == username {
            violations[i].Count++
            found = true
            
            if violations[i].Count >= 10 {
                mu.Unlock()
                w.Write([]byte("MAX_VIOLATIONS"))
                return
            }
            
            w.Write([]byte(fmt.Sprintf("VIOLATION:WINDOW_CHANGE_VIOLATION:%d", violations[i].Count)))
            mu.Unlock()
            return
        }
    }
    
    if !found {
        violations = append(violations, Violation{Username: username, Count: 1})
        w.Write([]byte(fmt.Sprintf("VIOLATION:WINDOW_CHANGE_VIOLATION:1")))
    }
    mu.Unlock()
}

func submitHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        w.WriteHeader(http.StatusBadRequest)
        return
    }

    username := r.FormValue("username")
    score := r.FormValue("score")

    var sc int
    fmt.Sscanf(score, "%d", &sc)

    mu.Lock()
    results = append(results, Result{Username: username, Score: sc})
    mu.Unlock()

    http.Redirect(w, r, "/score?user="+username, http.StatusSeeOther)
}